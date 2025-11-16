package main

import (
	"context"
	"ptzposetracker"

	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	generic "go.viam.com/rdk/services/generic"
)

func main() {
	err := realMain()
	if err != nil {
		panic(err)
	}
}

func realMain() error {
	ctx := context.Background()
	logger := logging.NewLogger("cli")

	deps := resource.Dependencies{}
	// can load these from a remote machine if you need

	cfg := ptzposetracker.Config{
		TargetPoseName:    "target_pose_name",
		PTZCameraName:     "ptz_camera",
		UpdateRateHz:      10.0,
		PanSpeedAngleDeg:  1.0,
		TiltSpeedAngleDeg: 1.0,
		ZoomMode:          "fixed",
		FixedZoom:         1.0,
		EnableOnStart:     true,
	}

	thing, err := ptzposetracker.NewTracker(ctx, deps, generic.Named("foo"), &cfg, logger)
	if err != nil {
		return err
	}
	defer thing.Close(ctx)

	return nil
}
