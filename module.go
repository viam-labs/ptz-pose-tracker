package ptzposetracker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/erh/vmodutils"
	"github.com/erh/vmodutils/touch"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/referenceframe"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/robot"
	generic "go.viam.com/rdk/services/generic"
)

var (
	PtzArmPoseTracker = resource.NewModel("viamlabs", "ptz-pose-tracker", "ptz-arm-pose-tracker")
	errUnimplemented  = errors.New("unimplemented")
)

func init() {
	resource.RegisterService(generic.API, PtzArmPoseTracker,
		resource.Registration[resource.Resource, *Config]{
			Constructor: newPtzPoseTrackerPtzArmPoseTracker,
		},
	)
}

type Config struct {
	TargetPoseName    string  `json:"target_pose_name"`
	PTZCameraName     string  `json:"ptz_camera_name"`
	UpdateRateHz      float64 `json:"update_rate_hz"`
	PanSpeedAngleDeg  float64 `json:"pan_speed_angle_deg"`
	TiltSpeedAngleDeg float64 `json:"tilt_speed_angle_deg"`
	ZoomMode          string  `json:"zoom_mode"`
	FixedZoom         float64 `json:"fixed_zoom"`
	EnableOnStart     bool    `json:"enable_on_start"`
}

// Validate ensures all parts of the config are valid and important fields exist.
// Returns implicit required (first return) and optional (second return) dependencies based on the config.
// The path is the JSON path in your robot's config (not the `Config` struct) to the
// resource being validated; e.g. "components.0".
func (cfg *Config) Validate(path string) ([]string, []string, error) {
	// Add config validation code here
	if cfg.TargetPoseName == "" {
		return nil, nil, errors.New("target_pose_name is required")
	}
	if cfg.PTZCameraName == "" {
		return nil, nil, errors.New("ptz_camera_name is required")
	}
	if cfg.UpdateRateHz <= 0 {
		return nil, nil, errors.New("update_rate_hz must be greater than 0")
	}
	if cfg.PanSpeedAngleDeg <= 0 {
		return nil, nil, errors.New("pan_speed_angle_deg must be greater than 0")
	}
	if cfg.TiltSpeedAngleDeg <= 0 {
		return nil, nil, errors.New("tilt_speed_angle_deg must be greater than 0")
	}
	if cfg.ZoomMode != "fixed" && cfg.ZoomMode != "auto" {
		return nil, nil, errors.New("zoom_mode must be either 'fixed' or 'auto'")
	}
	if cfg.ZoomMode == "fixed" && cfg.FixedZoom <= 0 {
		return nil, nil, errors.New("fixed_zoom must be greater than 0")
	}
	return nil, nil, nil
}

type ptzPoseTrackerPtzArmPoseTracker struct {
	resource.AlwaysRebuild

	name resource.Name

	logger logging.Logger
	cfg    *Config

	cancelCtx  context.Context
	cancelFunc func()

	robotClient    robot.Robot
	targetPoseName string
}

func newPtzPoseTrackerPtzArmPoseTracker(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (resource.Resource, error) {
	conf, err := resource.NativeConfig[*Config](rawConf)
	if err != nil {
		return nil, err
	}

	return NewPtzArmPoseTracker(ctx, deps, rawConf.ResourceName(), conf, logger)
}

func NewPtzArmPoseTracker(ctx context.Context, deps resource.Dependencies, name resource.Name, conf *Config, logger logging.Logger) (resource.Resource, error) {

	cancelCtx, cancelFunc := context.WithCancel(context.Background())

	robotClient, err := vmodutils.ConnectToMachineFromEnv(ctx, logger)
	if err != nil {
		cancelFunc()
		return nil, fmt.Errorf("failed to connect to robot: %w", err)
	}

	s := &ptzPoseTrackerPtzArmPoseTracker{
		name:           name,
		logger:         logger,
		cfg:            conf,
		cancelCtx:      cancelCtx,
		cancelFunc:     cancelFunc,
		robotClient:    robotClient,
		targetPoseName: conf.TargetPoseName,
	}

	if conf.EnableOnStart {
		go s.trackingLoop(s.cancelCtx)
		s.logger.Info("PTZ pose tracker started")
	}

	return s, nil
}

func (s *ptzPoseTrackerPtzArmPoseTracker) Name() resource.Name {
	return s.name
}

func (s *ptzPoseTrackerPtzArmPoseTracker) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *ptzPoseTrackerPtzArmPoseTracker) Close(context.Context) error {
	// Put close code here
	s.cancelFunc()
	return nil
}

func (t *ptzPoseTrackerPtzArmPoseTracker) trackingLoop(ctx context.Context) {
	t.logger.Info("Starting tracking loop")
	t.logger.Info("Update rate: %f Hz", t.cfg.UpdateRateHz)
	var updateInterval time.Duration = time.Duration(1.0 / t.cfg.UpdateRateHz * float64(time.Second))
	t.logger.Info("Update interval: %v", updateInterval)
	ticker := time.NewTicker(updateInterval)
	defer ticker.Stop()

	fsc, err := t.robotClient.FrameSystemConfig(ctx)
	if err != nil {
		t.logger.Error("Failed to get frame system config: %v", err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 1. Get the target pose in the frame system
			targetPosePart := touch.FindPart(fsc, t.targetPoseName)
			if targetPosePart == nil {
				t.logger.Error("can't find frame for %v", t.targetPoseName)
				continue
			}
			t.logger.Info("Target pose part: %v", targetPosePart)

			cameraPart := touch.FindPart(fsc, t.cfg.PTZCameraName)
			if cameraPart == nil {
				t.logger.Error("can't find frame for %v", t.cfg.PTZCameraName)
				continue
			}
			t.logger.Info("Camera part: %v", cameraPart)

			targetPose := targetPosePart.FrameConfig.PoseInFrame
			if targetPose == nil {
				t.logger.Error("can't get pose for %v", t.targetPoseName)
				continue
			}
			t.logger.Info("Target pose: %v", targetPose)

			ptzCameraPose := cameraPart.FrameConfig.PoseInFrame
			if ptzCameraPose == nil {
				t.logger.Error("can't get pose for %v", t.cfg.PTZCameraName)
				continue
			}
			t.logger.Info("PTZ camera pose: %v", ptzCameraPose)

			targetPoseInCameraFrame, err := t.robotClient.TransformPose(ctx, targetPose, cameraPart.FrameConfig.Name(), []*referenceframe.LinkInFrame{})
			if err != nil {
				t.logger.Error("Failed to transform pose: %v", err)
				continue
			}
			t.logger.Info("Target pose in camera frame: %v", targetPoseInCameraFrame)

			targetPoseInCameraFramePose := targetPoseInCameraFrame.Pose()
			t.logger.Info("Target pose in camera frame pose: %v", targetPoseInCameraFramePose)

			// 3. Calculate pan/tilt angles needed to center the end effector in the PTZ camera frame
			pan, tilt := t.calculatePanTilt(targetPoseInCameraFrame, ptzCameraPose)

			// 4. Calculate zoom based on distance (optional)
			zoom := t.calculateZoom(targetPose, ptzCameraPose)

			// 5. Send relative move command to PTZ
			err = t.movePTZ(ctx, pan, tilt, zoom)
			if err != nil {
				t.logger.Error("Failed to move PTZ: %v", err)
			}
		}
	}
}

func (t *ptzPoseTrackerPtzArmPoseTracker) calculatePanTilt(targetPoseInCameraFrame *referenceframe.PoseInFrame, ptzCameraPose *referenceframe.PoseInFrame) (float64, float64) {
	t.logger.Info("Calculating pan and tilt")
	return 0, 0
}

func (t *ptzPoseTrackerPtzArmPoseTracker) calculateZoom(targetPose *referenceframe.PoseInFrame, ptzCameraPose *referenceframe.PoseInFrame) float64 {
	t.logger.Info("Calculating zoom")
	return 0
}

func (t *ptzPoseTrackerPtzArmPoseTracker) movePTZ(ctx context.Context, pan float64, tilt float64, zoom float64) error {
	t.logger.Info("Moving PTZ")
	return nil
}
