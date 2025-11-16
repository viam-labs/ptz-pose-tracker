package ptzposetracker

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	Tracker          = resource.NewModel("viamlabs", "ptz-pose-tracker", "tracker")
	errUnimplemented = errors.New("unimplemented")
)

func init() {
	resource.RegisterService(generic.API, Tracker,
		resource.Registration[resource.Resource, *Config]{
			Constructor: newTracker,
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

type tracker struct {
	resource.AlwaysRebuild

	name resource.Name

	logger logging.Logger
	cfg    *Config

	cancelCtx  context.Context
	cancelFunc func()

	robotClient    robot.Robot
	targetPoseName string
}

func newTracker(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (resource.Resource, error) {
	conf, err := resource.NativeConfig[*Config](rawConf)
	if err != nil {
		return nil, err
	}

	return NewTracker(ctx, deps, rawConf.ResourceName(), conf, logger)
}

func NewTracker(ctx context.Context, deps resource.Dependencies, name resource.Name, conf *Config, logger logging.Logger) (resource.Resource, error) {

	cancelCtx, cancelFunc := context.WithCancel(context.Background())

	robotClient, err := vmodutils.ConnectToMachineFromEnv(ctx, logger)
	if err != nil {
		cancelFunc()
		return nil, fmt.Errorf("failed to connect to robot: %w", err)
	}

	s := &tracker{
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

func (s *tracker) Name() resource.Name {
	return s.name
}

func (s *tracker) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *tracker) Close(context.Context) error {
	// Put close code here
	s.cancelFunc()
	return nil
}

func (t *tracker) trackingLoop(ctx context.Context) {
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
			targetFramePart := touch.FindPart(fsc, t.targetPoseName)
			if targetFramePart == nil {
				t.logger.Errorf("can't find frame for %v", t.targetPoseName)
				continue
			}
			targetPose, err := t.robotClient.GetPose(ctx, targetFramePart.FrameConfig.Name(), "", []*referenceframe.LinkInFrame{}, map[string]interface{}{})
			if err != nil {
				t.logger.Errorf("Failed to get pose: %v", err)
				continue
			}
			t.logger.Infof("Target pose: %+v", targetPose)

			cameraFramePart := touch.FindPart(fsc, t.cfg.PTZCameraName)
			if cameraFramePart == nil {
				t.logger.Errorf("can't find frame for %v", t.cfg.PTZCameraName)
				continue
			}
			cameraPose, err := t.robotClient.GetPose(ctx, cameraFramePart.FrameConfig.Name(), "", []*referenceframe.LinkInFrame{}, map[string]interface{}{})
			if err != nil {
				t.logger.Errorf("Failed to get pose: %v", err)
				continue
			}
			t.logger.Infof("Camera pose: %+v", cameraPose)

			targetPoseInCameraFrame, err := t.robotClient.TransformPose(ctx, targetPose, cameraFramePart.FrameConfig.Name(), []*referenceframe.LinkInFrame{})
			if err != nil {
				t.logger.Errorf("Failed to transform target pose to camera frame: %v", err)
				continue
			}
			t.logger.Infof("Target pose in camera frame: %+v", targetPoseInCameraFrame)

			// Log position details for debugging
			pos := targetPoseInCameraFrame.Pose().Point()
			t.logger.Infof("Target position relative to camera: X=%.1f (right+), Y=%.1f (up+), Z=%.1f (forward+)",
				pos.X, pos.Y, pos.Z)

			// 3. Calculate pan/tilt angles needed to center the target in the PTZ camera frame
			pan, tilt, zoom := t.calculatePanTiltZoom(targetPoseInCameraFrame)
			t.logger.Infof("Pan: %f, Tilt: %f, Zoom: %f", pan, tilt, zoom)

			// 4. Send relative move command to PTZ
			err = t.movePTZ(ctx, pan, tilt, zoom)
			if err != nil {
				t.logger.Errorf("Failed to move PTZ: %v", err)
			}
		}
	}
}

func (t *tracker) calculatePanTiltZoom(targetPoseInCameraFrame *referenceframe.PoseInFrame) (float64, float64, float64) {
	t.logger.Infof("Calculating pan and tilt")
	t.logger.Infof("Target pose in camera frame: %+v", targetPoseInCameraFrame)

	// Position relative to camera
	x := targetPoseInCameraFrame.Pose().Point().X // Right/Left (positive = right)
	y := targetPoseInCameraFrame.Pose().Point().Y // Up/Down (positive = up)
	z := targetPoseInCameraFrame.Pose().Point().Z // Forward/Back (positive = forward)

	// Check if target is behind the camera
	if z < 0 {
		t.logger.Warnf("Target is behind camera (Z=%.1f). Cannot track.", z)
		return 0, 0, 0
	}

	// Calculate distance for zoom
	distance := math.Sqrt(x*x + y*y + z*z)

	// Calculate horizontal distance (in XZ plane) for tilt calculation
	horizontalDist := math.Sqrt(x*x + z*z)

	// Calculate angles in degrees
	// Pan: rotation around vertical axis (positive = rotate right)
	pan := math.Atan2(x, z) * 180.0 / math.Pi

	// Tilt: rotation around horizontal axis (positive = rotate up)
	// Use horizontal distance to ensure tilt stays in valid range [-90, 90]
	tilt := math.Atan2(y, horizontalDist) * 180.0 / math.Pi

	// Calculate zoom based on distance (you'll need to tune this mapping)
	// For now, using a simple inverse relationship: closer = more zoom
	// This will need adjustment based on your specific needs
	zoom := distance

	t.logger.Infof("Distance: %.1fmm, Pan: %.1f°, Tilt: %.1f°", distance, pan, tilt)

	return pan, tilt, zoom
}

/*
Move the PTZ camera to the given pan, tilt, and zoom.
From Viam RTSP PTZ documentation:
Notes
Disclaimer: This model was made in order to fully integrate with one specific camera. I tried to generalize it to all PTZ cameras, but your mileage may vary.
Profile Discovery: Use get-profiles command to discover valid profile tokens
Coordinate Spaces:
Normalized: -1.0 to 1.0 (pan/tilt), 0.0-1.0 (zoom)
Degrees: -180° to 180° (pan), -90° to 90° (tilt)
Absolute Moves: Use normalized coordinates (-1.0 to 1.0 for pan/tilt, 0.0 to 1.0 for zoom).
Relative Moves:
Normalized (degrees: false): -1.0 to 1.0 (pan/tilt/zoom).
Degrees (degrees: true): -180° to 180° (pan), -90° to 90° (tilt). Zoom remains normalized.
Movement Speeds:
Continuous: -1.0 (full reverse) to 1.0 (full forward).
Relative/Absolute: Speed parameters (pan_speed, tilt_speed, zoom_speed between 0.0 and 1.0) are optional. If no speed parameters are provided, the camera uses its default speed. If any speed parameter is provided, the Speed element is included in the request (using defaults of 0.5 for Relative or 1.0 for Absolute for any unspecified speed components).
*/

func (t *tracker) movePTZ(ctx context.Context, pan float64, tilt float64, zoom float64) error {
	t.logger.Infof("Moving PTZ")
	t.logger.Infof("Pan: %f, Tilt: %f, Zoom: %f", pan, tilt, zoom)

	// Convert pan and tilt to normalized coordinates
	panNormalized := pan / 180.0  // -1.0 to 1.0
	tiltNormalized := tilt / 90.0 // -1.0 to 1.0

	// Convert zoom to normalized coordinates
	// In order to do this, we need to know the range of the zoom of the PTZ camera.
	zoomNormalized := zoom / 1.0 // 0.0 to 1.0

	t.logger.Infof("Pan normalized: %f, Tilt normalized: %f, Zoom normalized: %f", panNormalized, tiltNormalized, zoomNormalized)

	return nil
}
