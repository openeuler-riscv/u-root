package fwupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"github.com/u-root/u-root/pkg/sh"
)

type FWUpdateMTD struct {
	rootdir		string
	device		string
	firmware_path	string

	firmware_buffer	[]byte
}

// Implement parse() from FWUpdateMethod
func (upd *FWUpdateMTD) parse(directive string, argument string) error {
	switch directive {
		case "file":
			upd.firmware_path = argument
		case "device":
			upd.device = argument
		default:
			return fmt.Errorf("uncognized directive: %s", directive)
	}
	return nil
}

// Implement check() from FWUpdateMethod
func (upd FWUpdateMTD) check() error {
	if upd.firmware_path == "" {
		return fmt.Errorf("state error: file not defined")
	}
	if upd.device == "" {
		return fmt.Errorf("state error: device not defined")
	}
	return nil
}

// Implement load() from FWUpdateMethod
func (upd *FWUpdateMTD) load() error {
	var err error
	upd.firmware_buffer, err = os.ReadFile(filepath.Join(upd.rootdir, upd.firmware_path))
	if err != nil {
		return fmt.Errorf("failed to read firmware: %w", err)
	}
	return nil
}

// Implement perform() from FWUpdateMethod
func (upd FWUpdateMTD) perform() error {
	f, err := os.CreateTemp("", "fwupdate")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(upd.firmware_buffer); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return sh.RunWithLogs("/bbin/mtdupdate", upd.device, f.Name())
}
