//go:build !windows

package clipboard

import (
	"image"

	"github.com/tradalab/scorix/fault"
)

func writeImage(image.Image) error {
	return fault.New(fault.CodeUnavailable, "image clipboard is only implemented on Windows")
}

func readImage() (image.Image, error) {
	return nil, fault.New(fault.CodeUnavailable, "image clipboard is only implemented on Windows")
}
