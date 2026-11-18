// Command pcma-prepare produces one derived G-code artifact for a PC execution.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"pcma.local/moonraker/gcode"
)

func run() error {
	fmt.Fprintln(os.Stderr, "PrusaConnectMoonrakerAdapter by D3ADD3V3R. Built on Prusa Connect Local by Prusa Research a.s. (https://www.prusa3d.com).")
	input := flag.String("input", "", "approved source G-code")
	output := flag.String("output", "", "new derived G-code path")
	execution := flag.String("execution", "", "unique execution ID")
	profile := flag.String("profile", "", "approved compatibility profile ID")
	version := flag.String("profile-version", "", "approved compatibility profile revision")
	material := flag.String("material", "", "approved material ID, or slicer metadata")
	color := flag.String("color", "", "approved color/spool ID, or slicer metadata")
	flag.Parse()
	if *input == "" || *output == "" {
		return fmt.Errorf("input and output are required")
	}
	source, err := os.Open(*input)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = target.Close()
		if !complete {
			_ = os.Remove(*output)
		}
	}()
	artifact, err := gcode.Prepare(source, target, gcode.Options{ExecutionID: *execution, ProfileID: *profile, ProfileVersion: *version, Material: *material, Color: *color})
	if err != nil {
		return err
	}
	if err := target.Sync(); err != nil {
		return err
	}
	if err := target.Close(); err != nil {
		return err
	}
	complete = true
	return json.NewEncoder(os.Stdout).Encode(artifact)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
