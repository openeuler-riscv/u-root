// Copyright 2022 the u-root Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package image contains a parser for RISCV64 Linux Image format.
package image

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	// Magic values used in Image header.
	Magic = 0x5643534952
	Magic2 = 0x05435352
)

var (
	kernelImageSize = uint64(math.Pow(2, 24)) // 16MB, a guess value similar to that used in kexec-tools.

	errBadMagic     = errors.New("bad header magic")
	errBadMagic2     = errors.New("bad header magic2")
	errBadEndianess = errors.New("invalid Image endianess, expected little")
)

// Arm64Header is header for Arm64 Image.
type RISCV64Header struct {
	Code0      uint32 `offset:"0x00"`
	Code1      uint32 `offset:"0x04"`
	TextOffset uint64 `offset:"0x08"`
	ImageSize  uint64 `offset:"0x10"`
	Flags      uint64 `offset:"0x18"`
	Version    uint32 `offset:"0x20"`
	Res1       uint32 `offset:"0x24"`
	Res2       uint64 `offset:"0x28"`
	Magic      uint64 `offset:"0x30"`
	Magic2     uint32 `offset:"0x38"`
	Res3       uint32 `offset:"0x3c"`
}

// Image abstracts Arm64 Image.
type Image struct {
	Header RISCV64Header
	Data   []byte
}

// ParseFromBytes parse an Image from bytes slice.
func ParseFromBytes(data []byte) (*Image, error) {
	img := &Image{}

	if err := binary.Read(bytes.NewBuffer(data), binary.LittleEndian, &img.Header); err != nil {
		return img, fmt.Errorf("unmarshaling riscv64 header: %w", err)
	}

	if img.Header.Magic != Magic {
		return img, errBadMagic
	}

	if img.Header.Magic2 != Magic2 {
		return img, errBadMagic2
	}

	if img.Header.ImageSize == 0 {
		/* For 3.16 and older kernels. */
		img.Header.TextOffset = 0x80000
		img.Header.ImageSize = kernelImageSize
	}

	// NOTE(10000TB): For now assumes and support little endian riscv64.
	// Error out if Image is not little endian.
	if int(img.Header.Flags&0x1) != 0 {
		return img, errBadEndianess
	}

	img.Data = data

	return img, nil
}
