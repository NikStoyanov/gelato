package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/NikStoyanov/gelato/lib"
	"github.com/urfave/cli"
)

var p *lib.Project

func runSetup() {
	// Create Machine
	var m lib.Machine
	m = lib.Machine{
		Number:        2,
		StartupScript: "#! /bin/bash\n\ncat <<EOF > ~/setup\nStartup script!",
	}

	// Create project
	p = &lib.Project{
		ProjectID:    "steel-melody-215909",
		BucketName:   "gelato-1571825101",
		StorageClass: "COLDLINE",
		Location:     "asia",
		MachineSetup: m,
	}

	ctx := context.Background()
	p.SetupDefaults()
	fmt.Println(p.SetupBucket(ctx))
	fmt.Println(p.SetupMsg(ctx))
	fmt.Println(p.StartPreVM(ctx))
}

func stopSetup() {
	// Stop the running VM instances.
	ctx := context.Background()
	p.StopVM(ctx)
}

func main() {
	app := cli.NewApp()
	app.Name = "gelato"
	app.Compiled = time.Now()
	app.Usage = "Deploy software to shortlived GCP instance for scientific computing."
	app.ArgsUsage = "[args and such]"
	app.EnableBashCompletion = true
	app.UseShortOptionHandling = true
	app.HideHelp = false
	app.HideVersion = false
	app.Action = func(c *cli.Context) error {
		// Pass setup file here
		runSetup()
		return nil
	}
	_ = app.Run(os.Args)
}
