package ptztracker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/erh/vmodutils"
	"github.com/erh/vmodutils/touch"
	"go.viam.com/rdk/components/generic"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/referenceframe"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/robot"
	genericservice "go.viam.com/rdk/services/generic"
)

var (
	PoseTracker      = resource.NewModel("viam-labs", "ptz-tracker", "pose-tracker")
	errUnimplemented = errors.New("unimplemented")
)

func init() {
	resource.RegisterService(genericservice.API, PoseTracker,
		resource.Registration[resource.Resource, *Config]{
			Constructor: newPoseTracker,
		},
	)
}

type Config struct {
	TargetPoseName     string     `json:"target_pose_name"`
	PTZCameraName      string     `json:"ptz_camera_name"`
	OnvifPTZClientName string     `json:"onvif_ptz_client_name"`
	UpdateRateHz       float64    `json:"update_rate_hz"`
	PanGain            float64    `json:"pan_gain"`     // Speed gain for pan (default 0.01 = 1°→1% speed)
	TiltGain           float64    `json:"tilt_gain"`    // Speed gain for tilt (default 0.01)
	MaxSpeed           float64    `json:"max_speed"`    // Max continuous speed (0.0-1.0, default 0.5)
	DeadbandDeg        float64    `json:"deadband_deg"` // Stop if error < this (degrees, default 2.0)
	ZoomMode           string     `json:"zoom_mode"`
	FixedZoom          float64    `json:"fixed_zoom"`
	ZoomRangeMts       [2]float64 `json:"zoom_range_mts"`
	ZoomSpeed          float64    `json:"zoom_speed"`
	EnableOnStart      bool       `json:"enable_on_start"`
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
	if cfg.OnvifPTZClientName == "" {
		return nil, nil, errors.New("onvif_ptz_client_name is required")
	}
	if cfg.UpdateRateHz <= 0 {
		return nil, nil, errors.New("update_rate_hz must be greater than 0")
	}
	// Set defaults for optional parameters
	if cfg.PanGain == 0 {
		cfg.PanGain = 0.01 // 1° error → 1% speed
	}
	if cfg.TiltGain == 0 {
		cfg.TiltGain = 0.01 // 1° error → 1% speed
	}
	if cfg.MaxSpeed == 0 {
		cfg.MaxSpeed = 0.5 // 50% max speed
	}
	if cfg.DeadbandDeg == 0 {
		cfg.DeadbandDeg = 2.0 // Stop within 2°
	}
	if cfg.ZoomSpeed == 0 {
		cfg.ZoomSpeed = 0.1 // Slow zoom for continuous mode
	}
	if cfg.ZoomRangeMts[0] <= 0 {
		return nil, nil, errors.New("zoom_range_mts[0] must be greater than 0")
	}
	if cfg.ZoomRangeMts[1] <= 0 {
		return nil, nil, errors.New("zoom_range_mts[1] must be greater than 0")
	}
	if cfg.ZoomRangeMts[0] >= cfg.ZoomRangeMts[1] {
		return nil, nil, errors.New("zoom_range_mts[0] must be less than zoom_range_mts[1]")
	}
	return nil, nil, nil
}

type poseTracker struct {
	resource.AlwaysRebuild

	name resource.Name

	logger logging.Logger
	cfg    *Config

	cancelCtx  context.Context
	cancelFunc func()

	robotClient        robot.Robot
	targetPoseName     string
	onvifPTZClientName string
	zoomRangeMinMts    float64
	zoomRangeMaxMts    float64
}

func newPoseTracker(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (resource.Resource, error) {
	conf, err := resource.NativeConfig[*Config](rawConf)
	if err != nil {
		return nil, err
	}

	return NewPoseTracker(ctx, deps, rawConf.ResourceName(), conf, logger)
}

func NewPoseTracker(ctx context.Context, deps resource.Dependencies, name resource.Name, conf *Config, logger logging.Logger) (resource.Resource, error) {

	cancelCtx, cancelFunc := context.WithCancel(context.Background())

	robotClient, err := vmodutils.ConnectToMachineFromEnv(ctx, logger)
	if err != nil {
		cancelFunc()
		return nil, fmt.Errorf("failed to connect to robot: %w", err)
	}

	s := &poseTracker{
		name:               name,
		logger:             logger,
		cfg:                conf,
		cancelCtx:          cancelCtx,
		cancelFunc:         cancelFunc,
		robotClient:        robotClient,
		targetPoseName:     conf.TargetPoseName,
		onvifPTZClientName: conf.OnvifPTZClientName,
		zoomRangeMinMts:    conf.ZoomRangeMts[0],
		zoomRangeMaxMts:    conf.ZoomRangeMts[1],
	}

	if conf.EnableOnStart {
		go s.trackingLoop(s.cancelCtx)
		s.logger.Info("PTZ pose tracker started")
	}

	return s, nil
}

func (s *poseTracker) Name() resource.Name {
	return s.name
}

func (s *poseTracker) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *poseTracker) Close(context.Context) error {
	// Put close code here
	s.cancelFunc()
	return nil
}

func (s *poseTracker) Status(ctx context.Context) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}

func (t *poseTracker) trackingLoop(ctx context.Context) {
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

func (t *poseTracker) calculatePanTiltZoom(targetPoseInCameraFrame *referenceframe.PoseInFrame) (float64, float64, float64) {
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

	t.logger.Infof("Distance: %.1fmm, Pan: %.1f°, Tilt: %.1f°", distance, pan, tilt)

	// For relative-move tracking, we return angles in degrees and normalized zoom
	// Pan and tilt represent how much the camera needs to rotate to point at the target

	// Convert zoom to normalized coordinates based on distance
	// Map distance range to zoom range (0.0 to 1.0)
	var zoomNormalized float64

	// Convert config values from meters to millimeters if needed
	zoomRangeMinMm := t.zoomRangeMinMts * 1000.0
	zoomRangeMaxMm := t.zoomRangeMaxMts * 1000.0
	zoomRange := zoomRangeMaxMm - zoomRangeMinMm

	t.logger.Infof("Zoom config: min=%.1fm (%.1fmm), max=%.1fm (%.1fmm), distance=%.1fmm",
		t.zoomRangeMinMts, zoomRangeMinMm, t.zoomRangeMaxMts, zoomRangeMaxMm, distance)

	if zoomRange <= 0 {
		// Invalid or unset zoom range, use a fixed zoom
		t.logger.Warnf("Invalid zoom range [%.1f, %.1f]mm, using fixed zoom", zoomRangeMinMm, zoomRangeMaxMm)
		zoomNormalized = 0.5 // Middle zoom level
	} else {
		// Normalize distance to 0-1 range
		// Closer distances (smaller values) = more zoom (higher values)
		// Farther distances (larger values) = less zoom (lower values)
		zoomNormalized = 1.0 - ((distance - zoomRangeMinMm) / zoomRange)

		// Clamp to valid range [0, 1]
		if zoomNormalized < 0.0 {
			t.logger.Infof("Distance %.1fmm below min %.1fmm, clamping zoom to 1.0", distance, zoomRangeMinMm)
			zoomNormalized = 1.0 // Max zoom (closest)
		} else if zoomNormalized > 1.0 {
			t.logger.Infof("Distance %.1fmm above max %.1fmm, clamping zoom to 0.0", distance, zoomRangeMaxMm)
			zoomNormalized = 0.0 // Min zoom (farthest)
		}
	}

	// For continuous-move, return angles as-is
	// movePTZ will convert them to speeds and handle deadband
	t.logger.Infof("Tracking error: Pan=%.2f°, Tilt=%.2f°, Zoom=%.3f", pan, tilt, zoomNormalized)

	return pan, tilt, zoomNormalized
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

func (t *poseTracker) movePTZ(ctx context.Context, panError float64, tiltError float64, zoomNormalized float64) error {
	onvifPTZClientName := resource.NewName(generic.API, t.onvifPTZClientName)
	onvifPTZClient, err := t.robotClient.ResourceByName(onvifPTZClientName)
	if err != nil {
		return fmt.Errorf("failed to get onvif PTZ client: %w", err)
	}

	// Check if we're within deadband (target is centered)
	panAbs := math.Abs(panError)
	tiltAbs := math.Abs(tiltError)

	if panAbs < t.cfg.DeadbandDeg && tiltAbs < t.cfg.DeadbandDeg {
		// Target is centered - stop movement
		t.logger.Infof("Target centered (Pan=%.2f°, Tilt=%.2f° < %.2f° deadband) - Stopping",
			panError, tiltError, t.cfg.DeadbandDeg)

		_, err := onvifPTZClient.DoCommand(ctx, map[string]interface{}{
			"command":  "stop",
			"pan_tilt": true,
			"zoom":     false,
		})
		if err != nil {
			t.logger.Errorf("Failed to stop PTZ: %v", err)
			return err
		}
		return nil
	}

	// Convert angle error to speed (-1.0 to 1.0)
	panSpeed := panError * t.cfg.PanGain
	tiltSpeed := tiltError * t.cfg.TiltGain

	// Clamp to max speed
	if panSpeed > t.cfg.MaxSpeed {
		panSpeed = t.cfg.MaxSpeed
	} else if panSpeed < -t.cfg.MaxSpeed {
		panSpeed = -t.cfg.MaxSpeed
	}

	if tiltSpeed > t.cfg.MaxSpeed {
		tiltSpeed = t.cfg.MaxSpeed
	} else if tiltSpeed < -t.cfg.MaxSpeed {
		tiltSpeed = -t.cfg.MaxSpeed
	}

	t.logger.Infof("Moving PTZ: Pan speed=%.3f, Tilt speed=%.3f, Zoom speed=%.3f",
		panSpeed, tiltSpeed, t.cfg.ZoomSpeed)

	// Use continuous-move - camera moves continuously at these speeds
	ptzMovementResponse, err := onvifPTZClient.DoCommand(ctx, map[string]interface{}{
		"command":    "continuous-move",
		"pan_speed":  panSpeed,
		"tilt_speed": tiltSpeed,
		"zoom_speed": t.cfg.ZoomSpeed,
	})
	if err != nil {
		t.logger.Errorf("Failed to move PTZ: %v", err)
		return err
	}
	t.logger.Debugf("PTZ movement response: %+v", ptzMovementResponse)
	return nil
}
