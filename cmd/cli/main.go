package main

import (
	"context"
	"ptztracker"

	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	genericservice "go.viam.com/rdk/services/generic"
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

	cfg := ptztracker.Config{
		TargetPoseName:     "target_pose_name",
		PTZCameraName:      "ptz_camera",
		OnvifPTZClientName: "onvif-ptz-client",
		UpdateRateHz:       10.0,
		PanGain:            0.01,
		TiltGain:           0.01,
		MaxSpeed:           0.5,
		DeadbandDeg:        2.0,
		ZoomMode:           "fixed",
		FixedZoom:          1.0,
		ZoomRangeMts:       [2]float64{0.01, 5.0},
		ZoomSpeed:          0.1,
		EnableOnStart:      true,
	}

	thing, err := ptztracker.NewPoseTracker(ctx, deps, genericservice.Named("foo"), &cfg, logger)
	if err != nil {
		return err
	}
	defer thing.Close(ctx)

	return nil
}
