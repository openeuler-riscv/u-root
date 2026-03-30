// mtdupdate can write to mtd devices.
package main

import (
	"flag"
	"unsafe"
	"os"
	"syscall"
	"log"
	"fmt"
	"bytes"
	"io"
	"github.com/u-root/u-root/pkg/uroot/util"
)

const usage = "mtdupdate <flash-device> <image-file>"

// Constants for MTD ioctl (from linux/mtd/mtd-abi.h)
const (
	MEMGETINFO = 0x80204d01 // _IOR('M', 1, struct mtd_info_user)
	MEMERASE   = 0x40084d02 // _IOW('M', 2, struct erase_info_user)
)

func init() {
	util.Usage(usage)
}

type mtdInfoUser struct {
	Type	  uint8
	Flags	 uint32
	Size	  uint32 // Total size of MTD
	Erasesize uint32 // Erase block size
	Writesize uint32
	Oobsize   uint32 // Out-of-band size per block
	Padding   uint64
}

type eraseInfoUser struct {
	Start uint32
	Length uint32
}

func ioctl(fd uintptr, req uint, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(req), uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// readMTDInfo reads MTD device info via ioctl MEMGETINFO
func readMTDInfo(f *os.File) (*mtdInfoUser, error) {
	var mi mtdInfoUser
	if err := ioctl(f.Fd(), MEMGETINFO, unsafe.Pointer(&mi)); err != nil {
		return nil, err
	}
	return &mi, nil
}

// eraseBlock erases flash at start with length erasesize
func eraseBlock(f *os.File, start, length uint32) error {
	ei := eraseInfoUser{Start: start, Length: length}
	return ioctl(f.Fd(), MEMERASE, unsafe.Pointer(&ei))
}

func main() {
	util.Usage(usage)
	flag.Parse()

	if flag.NArg() != 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <flash-device> <image-file>\n", os.Args[0])
		os.Exit(1)
	}

	flashPath := flag.Arg(0)
	imgPath := flag.Arg(1)

	img, err := os.Open(imgPath)
	if err != nil {
		log.Fatalf("Failed to open image file: %v", err)
	}
	defer img.Close()

	flash, err := os.OpenFile(flashPath, os.O_RDWR, 0)
	if err != nil {
		log.Fatalf("Failed to open flash device %s: %v", flashPath, err)
	}
	defer flash.Close()

	mtdInfo, err := readMTDInfo(flash)
	if err != nil {
		log.Fatalf("Failed to get MTD info: %v", err)
	}

	log.Printf("Flash device %s info: size=%d, eraseblock=%d", flashPath, mtdInfo.Size, mtdInfo.Erasesize)

	blockSize := int(mtdInfo.Erasesize)
	flashSize := int(mtdInfo.Size)

	// Read whole image
	imgData, err := io.ReadAll(img)
	if err != nil {
		log.Fatalf("Failed to read image data: %v", err)
	}

	imageSize := len(imgData)

	if imageSize > flashSize {
		log.Fatalf("Image size %d exceeds flash size %d", len(imgData), flashSize)
	}

	// Align image to block size
	if imageSize % blockSize != 0 {
		imgData = append(imgData, bytes.Repeat([]byte{0xff}, blockSize - imageSize % blockSize)...)
		imageSize = len(imgData)
	}

	bufReadFlash := make([]byte, blockSize)

	progressCount := 0
	skippedBlocks := 0
	for offset := 0; offset < imageSize; offset += blockSize {
		currentProgress := int((offset + blockSize) * 100 / imageSize)
		if currentProgress > progressCount {
			progressCount = currentProgress
			log.Printf("Progress: %3d%%", progressCount)
		}

		if _, err := flash.ReadAt(bufReadFlash, int64(offset)); err != nil {
			log.Fatalf("Failed to read flash at offset %x: %v", offset, err)
		}
		blockImage := imgData[offset : offset+blockSize]

		if bytes.Equal(bufReadFlash, blockImage) {
			skippedBlocks++
			continue
		}

		if err := eraseBlock(flash, uint32(offset), mtdInfo.Erasesize); err != nil {
			log.Fatalf("Failed to erase block at 0x%x: %v", offset, err)
		}

		n, err := flash.WriteAt(blockImage, int64(offset))
		if err != nil || n != blockSize {
			log.Fatalf("Failed to write flash at 0x%x: %v (written %d bytes)", offset, err, n)
		}

		if _, err := flash.ReadAt(bufReadFlash, int64(offset)); err != nil {
			log.Fatalf("Failed to read back flash at 0x%x: %v", offset, err)
		}

		if !bytes.Equal(bufReadFlash, blockImage) {
			log.Fatalf("Verification failed at block 0x%x", offset)
		}
	}

	log.Printf("Flash programming completed successfully, skipped %d/%d blocks(%d)", skippedBlocks, imageSize / blockSize, blockSize)
}
