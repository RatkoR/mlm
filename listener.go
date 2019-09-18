package main

import (
	"context"
	"errors"
	"fmt"
	mesos "github.com/mesos/mesos-go/api/v1/lib"
	"github.com/mesos/mesos-go/api/v1/lib/agent"
	"github.com/mesos/mesos-go/api/v1/lib/agent/calls"
	"github.com/mesos/mesos-go/api/v1/lib/httpcli"
	"github.com/mesos/mesos-go/api/v1/lib/httpcli/httpagent"
	"github.com/mesos/mesos-go/api/v1/lib/master"
	"net"
	"strconv"
	"strings"
	"time"
)

// Listener streams the content of a file
type Listener struct {
	agentSender calls.Sender
	task        mesos.Task
	agent       mesos.AgentInfo
	fileName    string
}

func NewListener(fileName string, task mesos.Task, agentInfo mesos.AgentInfo) *Listener {
	if task.AgentID.Value != agentInfo.ID.Value {
		panic("tasks agent id doesn't match provided agent info") // err? constructor should be safe though... MustNewListener ?
	}

	agentUrl := fmt.Sprintf("http://%s/api/v1", net.JoinHostPort(agentInfo.GetHostname(), strconv.Itoa(int(agentInfo.GetPort()))))
	agentSender := httpagent.NewSender(httpcli.New(httpcli.Endpoint(agentUrl)).Send)
	return &Listener{
		agentSender: agentSender,
		task:        task,
		fileName:    fileName,
		agent:       agentInfo,
	}
}

// Listen starts listening to the specified file and streams out the content
func (l *Listener) Listen(output chan string) error {
	// Get container info
	resp, err := l.agentSender.Send(context.TODO(), calls.NonStreaming(calls.GetContainers()))
	if err != nil {
		return err
	}
	var r agent.Response
	err = resp.Decode(&r)
	if err != nil {
		return err
	}

	containers := r.GetGetContainers().GetContainers()
	containerId := ""
	for _, c := range containers {
		if c.GetExecutorID().Value == l.task.GetTaskID().Value { // TODO assuming this is ok. Doublecheck
			containerId = c.GetContainerID().Value
			break
		}
	}

	// Get flags
	resp, err = l.agentSender.Send(context.TODO(), calls.NonStreaming(calls.GetFlags()))
	if err != nil {
		return err
	}
	err = resp.Decode(&r)
	if err != nil {
		return err
	}

	agentWorkDir := ""
	flags := r.GetGetFlags().GetFlags()
	for _, f := range flags {
		if f.GetName() == "work_dir" {
			agentWorkDir = f.GetValue()
		}
	}

	agentId := l.task.GetAgentID().Value
	frameworkId := l.task.GetFrameworkID().Value
	taskId := l.task.GetTaskID().Value

	if containerId == "" {
		return errors.New("container not found")
	}

	// {workdir}/slaves/{agentId}/frameworks/{frameworkId}/executors/{taskId}/runs/{containerId}/stdout
	fullPath := fmt.Sprintf("%s/slaves/%s/frameworks/%s/executors/%s/runs/%s/%s", agentWorkDir, agentId, frameworkId, taskId, containerId, l.fileName)
	offset := uint64(0)
	initial := true
	for {
		if initial {
			resp, err = l.agentSender.Send(context.TODO(), calls.NonStreaming(calls.ReadFileWithLength(fullPath, offset, 0))) // only to get the current size
		} else {
			resp, err = l.agentSender.Send(context.TODO(), calls.NonStreaming(calls.ReadFile(fullPath, offset))) // read to the end of the file
		}

		if err != nil {
			return err
		}

		var e master.Response

		err = resp.Decode(&e)
		if err != nil {
			return err
		}

		r := e.GetReadFile()

		// initial call to get size
		if offset == 0 {
			if r.GetSize() > 2000 {
				offset = r.GetSize() - 2000
			}
			initial = false
			continue
		} else {
			offset = r.GetSize()
		}

		data := r.GetData()

		if len(data) != 0 {
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				if len(strings.TrimSpace(line)) > 0 {
					// TODO use templates
					// TODO implement grep like filter. Use a channel to push the filter string to all listeners
					output <- fmt.Sprintf("[%s:%d]: %s",l.agent.Hostname, l.task.GetDiscovery().GetPorts().Ports[0].Number, line) //  l.task.GetTaskID().Value
				}
			}
		}
		// TODO sleep should be configurable
		time.Sleep(time.Duration(1000) * time.Millisecond)
	}
}
