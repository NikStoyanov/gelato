package lib

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

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

// Start a preemptive VM instance
func (p *Project) StartPreVM(ctx context.Context) MachineResponse {
	// Create Master node
	computeService, err := compute.NewService(ctx)

	if err != nil {
		return MachineResponse{err.Error()}
	}

	// Create Master VM
	p.Machines.Master.Name = fmt.Sprintf("%s-gelato-master", p.ProjectID)
	if err := p.createInstance(ctx, computeService, p.Machines.Master.Name); err != nil {
		return MachineResponse{err.Error()}
	}
	// Query Master VM to get IP address
	p.Machines.Master.IPAddress, err = p.getComputeInstID(ctx, computeService, p.Machines.Master.Name)
	if err != nil {
		return MachineResponse{err.Error()}
	}

	// Allocate slave machines
	p.Machines.Slave = make([]MachineAddress, 0)
	// Setup slave machines
	for i := 0; i < p.MachineSetup.Number; i++ {
		nSlaveName := fmt.Sprintf("%s-gelato-slave-%d", p.ProjectID, i)
		if err := p.createInstance(ctx, computeService, nSlaveName); err != nil {
			return MachineResponse{err.Error()}
		}
		nSlaveIP, err := p.getComputeInstID(ctx, computeService, nSlaveName)
		if err != nil {
			return MachineResponse{err.Error()}
		}

		// New slave machine
		nSlave := MachineAddress{
			Name:      nSlaveName,
			IPAddress: nSlaveIP,
		}

		// Add to the machine list
		p.Machines.Slave = append(p.Machines.Slave, nSlave)
	}

	return MachineResponse{"Success!"}
}

// createInstance starts a preemptive GCE Instance with configs specified by
// instance and which runs the specified startup script
func (p *Project) createInstance(ctx context.Context, computeService *compute.Service, MachineName string) error {

	// Setup image
	img, err := computeService.Images.GetFromFamily(p.MachineSetup.ImageProject,
		p.MachineSetup.ImageFamily).Context(ctx).Do()

	if err != nil {
		return err
	}

	// Setup instance
	op, err := computeService.Instances.Insert(p.ProjectID, p.Zone, &compute.Instance{
		MachineType: fmt.Sprintf("zones/%s/machineTypes/%s", p.Zone, p.MachineSetup.MType),
		Name:        MachineName,
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
