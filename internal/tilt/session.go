package tilt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
)

// Session is the identity reported by Tilt's Session resource. This API is
// used to observe an existing process, not to acquire control of it.
type Session struct {
	PID          int64
	StartTime    string
	TiltfilePath string
}

// Inspect queries the exact local Tilt API port recorded for a project and
// verifies the returned Tiltfile path before exposing its PID.
func (m *Manager) Inspect(ctx context.Context, config Config) (Session, error) {
	if config.Port==0||!filepath.IsAbs(config.Directory) {
		return Session{},errors.New("absolute project path and a Tilt HTTP port are required")
	}
	out,err:=m.launcher.Run(ctx,config,"get","session","-o","json","--host","127.0.0.1","--port",strconv.Itoa(int(config.Port)))
	if err!=nil{return Session{},fmt.Errorf("inspect Tilt session on port %d: %w",config.Port,err)}
	var response struct {
		Kind string `json:"kind"`
		Spec struct {TiltfilePath string `json:"tiltfilePath"`} `json:"spec"`
		Status struct {
			PID int64 `json:"pid"`
			StartTime string `json:"startTime"`
		} `json:"status"`
	}
	if err:=json.Unmarshal(out,&response);err!=nil{return Session{},fmt.Errorf("decode Tilt session: %w",err)}
	if response.Kind!="Session"||response.Status.PID<=0||response.Status.StartTime=="" {
		return Session{},fmt.Errorf("incomplete Tilt session identity on port %d",config.Port)
	}
	expected:=filepath.Join(config.Directory,"Tiltfile")
	if !filepath.IsAbs(response.Spec.TiltfilePath)||filepath.Clean(response.Spec.TiltfilePath)!=expected{
		return Session{},fmt.Errorf("Tilt session on port %d belongs to a different Tiltfile",config.Port)
	}
	return Session{PID:response.Status.PID,StartTime:response.Status.StartTime,TiltfilePath:expected},nil
}
