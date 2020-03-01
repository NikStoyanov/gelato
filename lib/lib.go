package lib

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/pubsub"
	"cloud.google.com/go/storage"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
)

// Project stores the name of the GCP project and bucket for storage
type Project struct {
	ProjectID    string
	BucketName   string
	StorageClass string
	Location     string
	Zone         string
	Scopes       string
	MsgTopic     *pubsub.Topic
	MachineSetup Machine
	Machines     MachineList
}

// Machine stores the definition of the virtual machine
type Machine struct {
	Number        int
	MType         string
	Duration      int
	StartupScript string
	ImageFamily   string
	ImageProject  string
}

// MachineResponse if the return result from operation on the virtual machine
type MachineResponse struct {
	response string
}

// MachineList is list of the active virtual machines
// TODO: add master and slave machines
// TODO: master needs to check if it will be stopped
// and create a new master
type MachineList struct {
	Master MachineAddress
	Slave  []MachineAddress
}

// MachineAddress stores the name and the IP of a machine
type MachineAddress struct {
	Name      string
	IPAddress string
}

// SetupDefaults check if all required values have been provided and if not
// fills them with defaults
func (p *Project) SetupDefaults() {
	// Bucket name
	if p.BucketName == "" {
		p.BucketName = fmt.Sprintf("gelato-%d", time.Now().Unix())
	}

	// Bucket storage class
	if p.StorageClass == "" {
		p.StorageClass = "COLDLINE"
	}

	// Bucket zone
	if p.Location == "" {
		p.Location = "europe"
	}

	// Image project
	if p.MachineSetup.ImageProject == "" {
		p.MachineSetup.ImageProject = "debian-cloud"
	}

	// Image family
	if p.MachineSetup.ImageFamily == "" {
		p.MachineSetup.ImageFamily = "debian-9"
	}

	// Machine type
	if p.MachineSetup.MType == "" {
		p.MachineSetup.MType = "n1-standard-1"
	}

	// Machine zone
	if p.Zone == "" {
		p.Zone = "europe-west2-c"
	}

	if p.MachineSetup.Number == 0 {
		p.MachineSetup.Number = 2
	}

	// Startup script - the only thing we cannot continue without
	if p.MachineSetup.StartupScript == "" {
		fmt.Println("You need an startup script!")
		os.Exit(1)
	}
}

func (p *Project) SetupBucket(ctx context.Context) MachineResponse {
	// Check if there are credentials provided, if not the client library
	// will look for credentials in the environment.
	storageClient, err := storage.NewClient(ctx)
	if err != nil {
		return MachineResponse{err.Error()}
	}

	if err := p.createBucket(ctx, storageClient); err != nil {
		return MachineResponse{err.Error()}
	}

	return MachineResponse{"Bucket storage in " + p.BucketName}
}

// Create a new bucket
func (p *Project) createBucket(ctx context.Context, client *storage.Client) error {
	bucket := client.Bucket(p.BucketName)

	if err := bucket.Create(ctx, p.ProjectID, &storage.BucketAttrs{
		StorageClass: p.StorageClass,
		Location:     p.Location,
	}); err != nil {
		// Check if bucket already exists under the current project
		if strings.Contains(err.Error(), "409") {
			return nil
		}
		return err
	}

	return nil
}

// Start a preemptive VM instance. First the master node is initiated and if successful the slave
// nodes are started concurrently.
func (p *Project) StartPreVM(ctx context.Context) MachineResponse {
	// Setup error channel and wait group for track the start of the new slave machines.
	errChannel := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(p.MachineSetup.Number)
	setupFin := make(chan bool, 1)

	// GCP does not create machines in the order at which they are requested. When a new machine
	// is started the information associated with is must be appended in a thread safe manner.
	// The mutex will prevent the race condition when a machine is becomes available.
	mux := &sync.Mutex{}

	// Create Master node.
	computeService, err := compute.NewService(ctx)
	if err != nil {
		return MachineResponse{err.Error()}
	}

	// Create Master VM and wait for it to be established. We need this node to be working
	// before we can proceed to the slaves nodes as they must exchange ssh keys.
	p.Machines.Master.Name = fmt.Sprintf("%s-gelato-master", p.ProjectID)
	if err := p.createInstance(ctx, computeService, p.Machines.Master.Name); err != nil {
		return MachineResponse{err.Error()}
	}

	// Query Master VM to get IP address. The address is then recorded to pubsub.
	p.Machines.Master.IPAddress, err = p.getComputeInstID(ctx, computeService, p.Machines.Master.Name)
	if err != nil {
		return MachineResponse{err.Error()}
	}

	// Record message in PubSub that the master node is started.
	// Add the IP server as an attribute
	// https://godoc.org/cloud.google.com/go/pubsub#Message
	p.PublishMsg(ctx, p.Machines.Master.Name)

	// Allocate slave machines.
	p.Machines.Slave = make([]MachineAddress, 0)

	// Concurrently setup slave machines.
	for machineNum := 0; machineNum < p.MachineSetup.Number; machineNum++ {
		go func(machineNum int) {
			// New slave machine ID.
			nSlaveName := fmt.Sprintf("%s-gelato-slave-%d", p.ProjectID, machineNum)

			// Create slave instace.
			if err := p.createInstance(ctx, computeService, nSlaveName); err != nil {
				errChannel <- err
			}

			// Obtain the assigned ID of the slave instance.
			nSlaveIP, err := p.getComputeInstID(ctx, computeService, nSlaveName)
			if err != nil {
				errChannel <- err
			}

			// Record new slave machine.
			nSlave := MachineAddress{
				Name:      nSlaveName,
				IPAddress: nSlaveIP,
			}

			mux.Lock()

			// Add to the machine list.
			p.Machines.Slave = append(p.Machines.Slave, nSlave)

			// Add creation message to PubSub.
			// TODO Send IP address in attributes
			if err := p.PublishMsg(ctx, nSlave.IPAddress); err != nil {
				errChannel <- err
			}

			mux.Unlock()
			wg.Done()
		}(machineNum)
	}

	// Wait for error to occur and either report it or wait for all of them to be closed.
	go func() {
		wg.Wait()
		close(setupFin)
	}()

	select {
	case <-setupFin:
	case err := <-errChannel:
		if err != nil {
			return MachineResponse{err.Error()}
		}
	}

	return MachineResponse{"Success!"}
}

// createInstance starts a preemptive GCE Instance with configs specified by
// instance and which runs the specified startup script
func (p *Project) createInstance(ctx context.Context, computeService *compute.Service, machineName string) error {

	// Setup image
	img, err := computeService.Images.GetFromFamily(p.MachineSetup.ImageProject,
		p.MachineSetup.ImageFamily).Context(ctx).Do()

	if err != nil {
		return err
	}

	// Setup instance
	op, err := computeService.Instances.Insert(p.ProjectID, p.Zone, &compute.Instance{
		MachineType: fmt.Sprintf("zones/%s/machineTypes/%s", p.Zone, p.MachineSetup.MType),
		Name:        machineName,
		Disks: []*compute.AttachedDisk{{
			AutoDelete: true, // delete the disk when the VM is deleted.
			Boot:       true,
			Type:       "PERSISTENT",
			Mode:       "READ_WRITE",
			InitializeParams: &compute.AttachedDiskInitializeParams{
				SourceImage: img.SelfLink,
				DiskType:    fmt.Sprintf("https://www.googleapis.com/compute/v1/projects/%s/zones/%s/diskTypes/pd-standard", p.ProjectID, p.Zone),
			},
		}},
		NetworkInterfaces: []*compute.NetworkInterface{{
			Network: fmt.Sprintf("https://www.googleapis.com/compute/v1/projects/%s/global/networks/default", p.ProjectID),
			AccessConfigs: []*compute.AccessConfig{{
				Name: "External NAT",
			}},
		}},
		Scheduling: &compute.Scheduling{
			Preemptible: true,
		},
		Metadata: &compute.Metadata{
			Items: []*compute.MetadataItems{{
				Key:   "startup-script",
				Value: googleapi.String(p.MachineSetup.StartupScript),
			}},
		},
	}).Do()

	if err != nil {
		return err
	}

	// Poll status of the operation to create the instance.
	getOpCall := computeService.ZoneOperations.Get(p.ProjectID, p.Zone, op.Name)
	for {
		if err := checkOpErrors(op); err != nil {
			return fmt.Errorf("failed to create instance: %v", err)
		}
		if op.Status == "DONE" {
			return nil
		}

		if err := gax.Sleep(ctx, 5*time.Second); err != nil {
			return err
		}

		op, err = getOpCall.Do()
		if err != nil {
			return fmt.Errorf("failed to get operation: %v", err)
		}
	}
}

// checkOpErrors returns nil if the operation does not have any errors and an
// error summarizing all errors encountered if the operation has errored.
func checkOpErrors(op *compute.Operation) error {
	if op.Error == nil || len(op.Error.Errors) == 0 {
		return nil
	}

	var errs []string
	for _, e := range op.Error.Errors {
		if e.Message != "" {
			errs = append(errs, e.Message)
		} else {
			errs = append(errs, e.Code)
		}
	}
	return errors.New(strings.Join(errs, ","))
}

// Get the external IP address of an instance
func (p *Project) getComputeInstID(ctx context.Context, computeService *compute.Service, machineName string) (string, error) {

	cpsReturn, err := computeService.Instances.Get(p.ProjectID, p.Zone, machineName).Do()

	if err != nil {
		return "", err
	}

	return cpsReturn.NetworkInterfaces[0].AccessConfigs[0].NatIP, nil
}

// https://cloud.google.com/compute/docs/reference/rest/v1/instances/list
// List registered VM instances.
func (p *Project) ListVM(ctx context.Context, computeService *compute.Service) *compute.InstanceList {
	req := computeService.Instances.List(p.ProjectID, p.Zone)
	var vms *compute.InstanceList
	if err := req.Pages(ctx, func(page *compute.InstanceList) error {
		vms = page
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	return vms
}

// Print the registered VM instances.
func (p *Project) PrintVM(ctx context.Context, computeService *compute.Service) MachineResponse {
	vms := p.ListVM(ctx, computeService)
	for _, instance := range vms.Items {
		fmt.Printf("%#v\n", instance)
	}

	return MachineResponse{"Success!"}
}

// Stop the running VM instances.
func (p *Project) StopVM(ctx context.Context, computeService *compute.Service) MachineResponse {
	vms := p.ListVM(ctx, computeService)
	for _, instance := range vms.Items {
		_, err := computeService.Instances.Stop(p.ProjectID, p.Zone, instance.Hostname).Context(ctx).Do()
		if err != nil {
			log.Fatal(err)
		}
	}

	return MachineResponse{"Success!"}
}

// Delete registered VM instances.
func (p *Project) DeleteVM(ctx context.Context, computeService *compute.Service) MachineResponse {
	vms := p.ListVM(ctx, computeService)
	for _, instance := range vms.Items {
		_, err := computeService.Instances.Delete(p.ProjectID, p.Zone, instance.Hostname).Context(ctx).Do()
		if err != nil {
			log.Fatal(err)
		}
	}

	return MachineResponse{"Success!"}
}
