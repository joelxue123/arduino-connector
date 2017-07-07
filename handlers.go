package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/kardianos/osext"
	"github.com/pkg/errors"
)

// StatusCB replies with the current status of the arduino-connector
func StatusCB(status *Status) mqtt.MessageHandler {
	return func(client mqtt.Client, msg mqtt.Message) {
		status.Publish()
	}
}

// UploadPayload contains the name and url of the sketch to upload on the device
type UploadPayload struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Name  string `json:"name"`
	Token string `json:"token"`
}

// UploadCB receives the url and name of the sketch binary, then it
// - downloads the binary,
// - chmods +x it
// - executes redirecting stdout and sterr to a proper logger
func UploadCB(status *Status) mqtt.MessageHandler {
	return func(client mqtt.Client, msg mqtt.Message) {
		// unmarshal
		var info UploadPayload
		err := json.Unmarshal(msg.Payload(), &info)
		if err != nil {
			status.Error("/upload", errors.Wrapf(err, "unmarshal %s", msg.Payload()))
			return
		}

		if info.ID == "" {
			info.ID = info.Name
		}

		// Stop and delete if existing
		if sketch, ok := status.Sketches[info.ID]; ok {
			err = applyAction(sketch, "STOP")
			if err != nil {
				status.Error("/upload", errors.Wrapf(err, "stop pid %d", sketch.PID))
				return
			}

			err = os.Remove(sketch.Name)
			if err != nil {
				status.Error("/upload", errors.Wrapf(err, "remove %d", sketch.Name))
				return
			}
		}

		// create folder
		folder, err := osext.ExecutableFolder()
		if err != nil {
			status.Error("/upload", errors.Wrapf(err, "create sketch folder %s", info.ID))
			return
		}

		// download the binary
		name := filepath.Join(folder, info.Name)
		err = downloadFile(name, info.URL, info.Token)
		if err != nil {
			status.Error("/upload", errors.Wrapf(err, "download file %s", info.URL))
			return
		}

		// chmod it
		err = os.Chmod(name, 0744)
		if err != nil {
			status.Error("/upload", errors.Wrapf(err, "chmod 744 %s", name))
			return
		}

		// spawn process
		pid, stdout, err := spawnProcess(name)
		if err != nil {
			status.Error("/upload", errors.Wrapf(err, "spawn %s", name))
			return
		}

		status.Info("/upload", "Sketch started with PID "+strconv.Itoa(pid))

		s := SketchStatus{
			ID:     info.ID,
			PID:    pid,
			Name:   info.Name,
			Status: "RUNNING",
		}
		status.Set(info.ID, s)
		status.Publish()

		go func(stdout io.ReadCloser) {
			in := bufio.NewScanner(stdout)
			for {
				for in.Scan() {
					fmt.Printf(in.Text()) // write each line to your log, or anything you need
				}
				fmt.Println("finished")
			}
		}(stdout)
	}
}

// downloadfile substitute a file with something that downloads from an url
func downloadFile(filepath, url, token string) error {
	// Create the file - remove the existing one if it exists
	if _, err := os.Stat(filepath); err == nil {
		err := os.Remove(filepath)
		if err != nil {
			return errors.Wrap(err, "remove "+filepath)
		}
	}
	out, err := os.Create(filepath)
	if err != nil {
		return errors.Wrap(err, "create "+filepath)
	}
	defer out.Close()
	// Get the data
	client := http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return errors.New("Expected OK, got " + resp.Status)
	}

	// Writer the body to file
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}
	return nil
}

// spawn Process creates a new process from a file
func spawnProcess(filepath string) (int, io.ReadCloser, error) {
	cmd := exec.Command(filepath)
	stdout, err := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return 0, stdout, err
	}
	return cmd.Process.Pid, stdout, err
}
