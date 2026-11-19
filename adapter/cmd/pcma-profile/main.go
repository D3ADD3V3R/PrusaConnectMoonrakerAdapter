// Command pcma-profile generates a capability profile from a printer's active configuration.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"pcma.local/moonraker"
)

func run() error {
	fmt.Fprintln(os.Stderr, "PrusaConnectMoonrakerAdapter by D3ADD3V3R. Built on Prusa Connect Local by Prusa Research a.s. (https://www.prusa3d.com).")
	address := flag.String("address", "", "Moonraker address")
	model := flag.String("model", "Klipper", "model label used by the PC endpoint")
	output := flag.String("output", "", "new profile JSON file")
	uploads := flag.Bool("uploads", false, "include verified upload support after a writable-root probe")
	flag.Parse()
	if *address == "" || *output == "" {
		return fmt.Errorf("address and output are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := moonraker.ReadConfigurationProfile(ctx, *address, *model, *uploads)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(*output)
		}
	}()
	if _, err := file.Write(append(report.Registry, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	return json.NewEncoder(os.Stdout).Encode(report)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
