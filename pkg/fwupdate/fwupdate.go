// Copyright 2020 the u-root Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package fwupdate contains helper functions for performing firmware updates.
package fwupdate

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/u-root/u-root/pkg/mount"
	"github.com/u-root/u-root/pkg/mount/block"
	"github.com/u-root/u-root/pkg/ulog"
)

type FWUpdate struct {
	label		string
	method		FWUpdateMethod
}

type FWUpdateMethod interface {
	parse (directive string, argument string) error
	check () error
	load () error
	perform () error
}

func (upd *FWUpdate) GetLabel() string {
	return upd.label
}

func (upd *FWUpdate) Load() error {
	return upd.method.load()
}

func (upd *FWUpdate) Perform() error {
	return upd.method.perform()
}

type FWUpdateParser struct {
	// fwEntries is a map of parsed entry id -> entry configuration.
	fwEntries map[string]FWUpdate

	// parser internals.
	rootdir string

	// parser state
	currID string
	currLabel string
	currEntry FWUpdateMethod
}

func splitFirstWord(s string) (firstWord, remainder string) {
    // Find the first index of space or tab
    idx := -1
    for i := 0; i < len(s); i++ {
        if s[i] == ' ' || s[i] == '\t' {
            idx = i
            break
        }
    }

    if idx == -1 {
        // No space or tab found, entire string is one word
        return s, ""
    }

    // Slice first word before whitespace, remainder after whitespace
    return s[:idx], strings.TrimSpace(s[idx+1:])
}

func (parser *FWUpdateParser) feedLine(line string) error {
	line = strings.TrimSpace(line)

	if line == "" { return nil }
	if strings.HasPrefix(line, "#") { return nil }

	directive, argument := splitFirstWord(line)
	directive = strings.ToLower(directive)

	switch directive {
		case "firmware":
			// unconditionally initialize first entry
			if parser.currID == "" {
				parser.currID = argument
				return nil
			}
			// new firmware entry specified
			// check existing one
			if parser.currLabel == "" {
				return fmt.Errorf("state error: label not defined")
			}
			if parser.currEntry == nil {
				return fmt.Errorf("state error: type not defined")
			}
			if err := parser.currEntry.check(); err != nil {
				return err
			}

			parser.fwEntries[parser.currID] = FWUpdate {
				label: parser.currLabel,
				method: parser.currEntry,
			}
			parser.currID = argument
			parser.currLabel = ""
			parser.currEntry = nil
		case "label":
			if parser.currID == "" {
				return fmt.Errorf("state error: firmware not defined")
			}
			parser.currLabel = argument
		case "type":
			if parser.currID == "" {
				return fmt.Errorf("state error: firmware not defined")
			}
			switch argument {
				case "mtd":
					parser.currEntry = &FWUpdateMTD {
						rootdir: parser.rootdir,
					}
				default:
					return fmt.Errorf("unsupported type: %s", argument)
			}
		default:
			if parser.currID == "" {
				return fmt.Errorf("state error: firmware not defined")
			}
			if parser.currEntry == nil {
				return fmt.Errorf("state error: type not defined")
			}
			return parser.currEntry.parse(directive, argument)
	}

	return nil
}

func parseFirmwareUpdates(l ulog.Logger, rootdir string) []FWUpdate {
	confPath := filepath.Join(rootdir, "firmware-update/firmware-update.conf")
	file, err := os.Open(confPath)
	if err != nil {
		l.Printf("Failed to open file: %s\n", confPath)
		return nil
	}
	defer file.Close()
	l.Printf("Got FWUpdate config: %s\n", confPath)

	parser := FWUpdateParser {
		rootdir: rootdir,
		fwEntries: make(map[string]FWUpdate),
	}

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if err := parser.feedLine(scanner.Text()); err != nil {
			l.Printf("Error parsing file: %w\n", err)
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		l.Printf("Error reading file: %w\n", err)
	}
	// an empty "firmware" directive will finish up previous section
	parser.feedLine("firmware")

	fwList := make([]FWUpdate, 0, len(parser.fwEntries))
	for _, v := range parser.fwEntries {
		fwList = append(fwList, v)
	}

	if len(fwList) == 0 {
		l.Printf("No FWUpdate found at: %s\n", rootdir)
	} else {
		l.Printf("%d FWUpdates found at: %s\n", len(fwList), rootdir)
	}

	return fwList
}

// LocalUpdate tries to fetch all configured firmware updates by scanning through block devices
func LocalUpdate(l ulog.Logger, blockDevs block.BlockDevices, mp *mount.Pool) ([]FWUpdate, error) {
	var updates []FWUpdate
	for _, device := range blockDevs {
		m, err := mp.Mount(device, mount.ReadOnly)
		if err != nil {
			continue
		}
		upds := parseFirmwareUpdates(l, m.Path)
		updates = append(updates, upds...)
	}

	return updates, nil
}
