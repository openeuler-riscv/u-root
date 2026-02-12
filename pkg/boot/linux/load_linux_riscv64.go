// Copyright 2022 the u-root Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package linux

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"log"
	"syscall"

	"github.com/u-root/u-root/pkg/boot/image"
	"github.com/u-root/u-root/pkg/boot/kexec"
	"github.com/u-root/u-root/pkg/dt"
	"github.com/u-root/u-root/pkg/uio"
	"golang.org/x/sys/unix"
)

const (
	kernelAlignSize = 1 << 21 // 2 MB.
)

func mmap(f *os.File) (data []byte, ummap func() error, err error) {
	s, err := f.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("stat error: %w", err)
	}
	if s.Size() == 0 {
		return nil, nil, fmt.Errorf("cannot mmap zero-len file")
	}
	d, err := unix.Mmap(int(f.Fd()), 0, int(s.Size()), syscall.PROT_READ, syscall.MAP_PRIVATE)
	if err != nil {
		return nil, nil, fmt.Errorf("mmap failed: %w", err)
	}

	ummap = func() error {
		return unix.Munmap(d)
	}

	return d, ummap, nil
}

// sanitizeFDT cleanups boot param properties from chosen node of the given FDT.
func sanitizeFDT(fdt *dt.FDT) (*dt.Node, error) {
	// Clear old entries in case we've already been through kexec to get
	// to this instance of runtime.
	chosen, _ := fdt.NodeByName("chosen")
	if chosen == nil {
		return nil, fmt.Errorf("no /chosen node in device tree")
	}
	for _, property := range []string{"linux,elfcorehdr", "linux,usable-memory-range", "kaslr-seed", "rng-seed", "linux,initrd-start", "linux,initrd-end"} {
		chosen.RemoveProperty(property)
	}

	return chosen, nil
}

func FixupFDTMemory(fdt_src *dt.FDT, fdt_dist *dt.FDT) (error) {
	addMemory := func(n *dt.Node) error {
		p, found := n.LookProperty("device_type")
		if !found {
			return nil
		}
		t, err := p.AsString()
		if err != nil || t != "memory" {
			return nil
		}
		p, found = n.LookProperty("reg")
		if found {
			// update existing node if found
			memory_node, _ := fdt_dist.NodeByName(n.Name)
			if memory_node == nil {
				memory_node = dt.NewNode(n.Name)
				fdt_dist.RootNode.Children = append(fdt_dist.RootNode.Children, memory_node)
			}
			for idx := range n.Properties {
				memory_node.UpdateProperty(n.Properties[idx].Name, n.Properties[idx].Value)
			}
		}
		return nil
	}
	err := fdt_src.RootNode.Walk(addMemory)
	if err != nil {
		return err
	}

	n, found := fdt_src.NodeByName("reserved-memory")
	if found {
		reserv_node, _ := fdt_dist.NodeByName(n.Name)
		if reserv_node == nil {
			reserv_node = dt.NewNode(n.Name)
			fdt_dist.RootNode.Children = append(fdt_dist.RootNode.Children, reserv_node)
		}
		for idx := range n.Properties {
			reserv_node.UpdateProperty(n.Properties[idx].Name, n.Properties[idx].Value)
		}
		reserv_node.Children = append(n.Children)
	}

	return nil
}

// KexecLoad loads arm64 Image, with the given ramfs and kernel cmdline.
func KexecLoad(kernel, ramfs *os.File, cmdline string, opts KexecOptions) error {
	var err error
	// kmem is a struct holding kexec segments.
	//
	// It has routines to work with physical memory
	// ranges.
	var kmem *kexec.Memory
	var kernelRange, ramfsRange, dtbRange, trampolineRange kexec.Range

	target_fdt, err := dt.ParseDTB(opts.DTB)
	if err != nil {
		return fmt.Errorf("ParseDTB = %v", err)
	}
	current_fdt, err := dt.LoadFDT("/sys/firmware/fdt")
	if err != nil {
		return fmt.Errorf("LoadFDT = %v", err)
	}

	err = FixupFDTMemory(current_fdt, target_fdt)
	if err != nil {
		return fmt.Errorf("FixupFDTMemory() = %v", err)
	}

	chosen, err := sanitizeFDT(target_fdt)
	if err != nil {
		return fmt.Errorf("sanitizeFDT(%v) = %v", target_fdt, err)
	}
	Debug("FDT after sanitization: %s", target_fdt)

	// Prepare segments.
	kmem = &kexec.Memory{}
	Debug("Try parsing memory map...")
	if err := kmem.ParseMemoryMapFromFDT(target_fdt); err != nil {
		return fmt.Errorf("ParseMemoryMapFromFDT(%v): %v", target_fdt, err)
	}
	Debug("Mem map: \n%+v", kmem.Phys)

	// Load kernel.
	var kernelBuf []byte
	if opts.MmapKernel {
		Debug("Mmapping kernel to virtual buffer...")
		var cleanup func() error
		kernelBuf, cleanup, err = mmap(kernel)
		if err != nil {
			return fmt.Errorf("mmap kernel: %v", err)
		}
		defer func() {
			if err = cleanup(); err != nil {
				Debug("Ummap kernel failed: %v", err)
			}
		}()
	} else {
		Debug("Read kernel from file ...")
		kernelBuf, err = uio.ReadAll(kernel)
		if err != nil {
			return fmt.Errorf("read kernel from file: %v", err)
		}
	}

	kImage, err := image.ParseFromBytes(kernelBuf)
	if err != nil {
		return fmt.Errorf("parse riscv64 Image from bytes: %v", err)
	}
	Debug("Image header version: %d.%d", kImage.Header.Version>>16, kImage.Header.Version&0xffff)

	if kernelRange, err = kmem.AddKexecSegmentExplicit(kernelBuf, uint(kImage.Header.ImageSize+kImage.Header.TextOffset), uint(kImage.Header.TextOffset), kernelAlignSize); err != nil {
		return fmt.Errorf("add kernel segment: %v", err)
	}

	Debug("Added %d byte (size %d) kernel at %s", len(kernelBuf), kImage.Header.ImageSize, kernelRange)

	var ramfsBuf []byte
	if ramfs != nil {
		if opts.MmapRamfs {
			Debug("Mmap ramfs file to virtual buffer...")
			var cleanup func() error
			ramfsBuf, cleanup, err = mmap(ramfs)
			if err != nil {
				return fmt.Errorf("mmap ramfs: %v", err)
			}
			defer func() {
				if err = cleanup(); err != nil {
					Debug("Ummap ramfs failed: %v", err)
				}
			}()
		} else {
			Debug("Read ramfs from file...")
			ramfsBuf, err = uio.ReadAll(ramfs)
			if err != nil {
				return fmt.Errorf("read ramfs from file: %v", err)
			}
		}
	}

	// NOTE(10000TB): This need be placed after kernel by convention.
	if ramfsRange, err = kmem.AddKexecSegment(ramfsBuf); err != nil {
		return fmt.Errorf("add initramfs segment: %v", err)
	}
	Debug("Added %d byte initramfs at %s", len(ramfsBuf), ramfsRange)

	ramfsStart := make([]byte, 8)
	binary.BigEndian.PutUint64(ramfsStart, uint64(ramfsRange.Start))
	chosen.UpdateProperty("linux,initrd-start", ramfsStart)
	ramfsEnd := make([]byte, 8)
	binary.BigEndian.PutUint64(ramfsEnd, uint64(ramfsRange.Start)+uint64(ramfsRange.Size))
	chosen.UpdateProperty("linux,initrd-end", ramfsEnd)

	Debug("Kernel cmdline to append: %s", cmdline)
	if len(cmdline) > 0 {
		cmdlineBuf := append([]byte(cmdline), byte(0))
		chosen.UpdateProperty("bootargs", cmdlineBuf)
	} else {
		chosen.RemoveProperty("bootargs")
	}

	dtbBuffer := &bytes.Buffer{}
	_, err = target_fdt.Write(dtbBuffer)
	if err != nil {
		return fmt.Errorf("flattening device tree: %v", err)
	}
	dtbBuf := dtbBuffer.Bytes()
	if dtbRange, err = kmem.AddKexecSegment(dtbBuf); err != nil {
		return fmt.Errorf("add device tree segment: %w", err)
	}
	Debug("Added %d byte device tree at %s", len(dtbBuf), dtbRange)

	// Trampoline.
	//
	// We need a trampoline to pass the DTB to the kernel; because
	// we'll use this code as our entry point, it also needs to know
	// the real entry point to kernel.
	//
	// TODO(10000TB): this assumes a little endian kernel, support
	// big endian if needed per flag.
	kernelEntry := kernelRange.Start
	dtbBase := dtbRange.Start

	var trampoline [8]uint32
	trampoline[0] = 0x00000297 // auipc	t0,0x0
	trampoline[1] = 0x0182b583 // ld	a1,24(t0) 	(trampoline[7 and 8])
	trampoline[2] = 0x0102b303 // ld	t1,16(t0)	(trampoline[5 and 6])
	trampoline[3] = 0x00030067 // jr	t1

	trampoline[4] = uint32(uint64(kernelEntry) & 0xffffffff)
	trampoline[5] = uint32(uint64(kernelEntry) >> 32)
	trampoline[6] = uint32(uint64(dtbBase) & 0xffffffff)
	trampoline[7] = uint32(uint64(dtbBase) >> 32)

	trampolineBuffer := new(bytes.Buffer)
	err = binary.Write(trampolineBuffer, binary.LittleEndian, trampoline)
	if err != nil {
		return fmt.Errorf("make trampoline: %v", err)
	}
	Debug("trampoline bytes %x", trampolineBuffer.Bytes())
	trampolineRange, err = kmem.AddKexecSegment(trampolineBuffer.Bytes())
	if err != nil {
		return fmt.Errorf("add trampoline segment: %v", err)
	}
	Debug("Added %d byte trampoline at %s", len(trampolineBuffer.Bytes()), trampolineRange)

	log.Printf("Trampoline Entry: %#x - %#x", trampolineRange.Start, uintptr(trampolineRange.Size) + trampolineRange.Start)
	log.Printf("Kernel Entry: %#x - %#x", kernelRange.Start, uintptr(kernelRange.Size) + kernelRange.Start)
	log.Printf("DTB Location: %#x - %#x", dtbRange.Start, uintptr(dtbRange.Size) + dtbRange.Start)
	log.Printf("Initramfs Location: %#x - %#x", ramfsRange.Start, uintptr(ramfsRange.Size) + ramfsRange.Start)

	/* Load it */
	entry := trampolineRange.Start
	if err = kexec.Load(entry, kmem.Segments, 0); err != nil {
		return fmt.Errorf("kexec Load(%v, %v, %d) = %v", entry, kmem.Segments, 0, err)
	}

	return nil
}
