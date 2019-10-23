package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/NikStoyanov/gelato/lib"
	"github.com/urfave/cli"
)

func runSetup() {
	// Create Machine
	var m lib.Machine
	m = lib.Machine{
		Number:        2,
		StartupScript: "curl -O https://julialang-s3.julialang.org/bin/linux/x64/1.2/julia-1.2.0-linux-x86_64.tar.gz | tar xfv julia-1.2.0-linux-x86_64.tar.gz",
	}

	// Create project
	var p *lib.Project
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
	fmt.Println(p.StartPreVM(ctx))
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
