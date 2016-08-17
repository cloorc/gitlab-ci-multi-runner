package kubernetes

import (
	"fmt"
	"io"
	"time"

	"golang.org/x/net/context"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/resource"
	"k8s.io/kubernetes/pkg/client/restclient"
	client "k8s.io/kubernetes/pkg/client/unversioned"

	"gitlab.com/gitlab-org/gitlab-ci-multi-runner/common"
)

func getKubeClientConfig(config *common.KubernetesConfig) (*restclient.Config, error) {
	switch {
	case len(config.CertFile) > 0:
		if len(config.KeyFile) == 0 || len(config.CAFile) == 0 {
			return nil, fmt.Errorf("ca file, cert file and key file must be specified when using file based auth")
		}
		return &restclient.Config{
			Host: config.Host,
			TLSClientConfig: restclient.TLSClientConfig{
				CertFile: config.CertFile,
				KeyFile:  config.KeyFile,
				CAFile:   config.CAFile,
			},
		}, nil
	case len(config.Host) > 0:
		return &restclient.Config{
			Host: config.Host,
		}, nil
	default:
		return restclient.InClusterConfig()
	}
}

func getKubeClient(config *common.KubernetesConfig) (*client.Client, error) {
	restConfig, err := getKubeClientConfig(config)
	if err != nil {
		return nil, err
	}

	return client.New(restConfig)
}

// waitForPodRunning will use client c to detect when pod reaches the PodRunning
// state. It will check every second, and will return the final PodPhase once
// either PodRunning, PodSucceeded or PodFailed has been reached. In the case of
// PodRunning, it will also wait until all containers within the pod are also Ready
// Returns error if the call to retreive pod details fails
func waitForPodRunning(ctx context.Context, c *client.Client, pod *api.Pod, out io.Writer) (api.PodPhase, error) {
	type resp struct {
		done  bool
		phase api.PodPhase
		err   error
	}
	for {
		select {
		case r := <-func() <-chan resp {
			errc := make(chan resp)
			go func() {
				defer close(errc)
				pod, err := c.Pods(pod.Namespace).Get(pod.Name)
				if err != nil {
					errc <- resp{true, api.PodUnknown, err}
					return
				}

				switch pod.Status.Phase {
				case api.PodRunning:
					errc <- resp{true, pod.Status.Phase, nil}
				case api.PodSucceeded:
					errc <- resp{true, pod.Status.Phase, fmt.Errorf("pod already succeeded before it begins running")}
				case api.PodFailed:
					errc <- resp{true, pod.Status.Phase, fmt.Errorf("pod status is failed")}
				default:
					fmt.Fprintf(out, "Waiting for pod %s/%s to be running, status is %s\n", pod.Namespace, pod.Name, pod.Status.Phase)
					time.Sleep(1 * time.Second)
					errc <- resp{false, pod.Status.Phase, nil}
				}
			}()
			return errc
		}():
			if r.done {
				return r.phase, r.err
			}
			continue
		case <-ctx.Done():
			return api.PodUnknown, ctx.Err()
		}
	}
}

// limits takes a string representing CPU & memory limits,
// and returns a ResourceList with appropriately scaled Quantity
// values for Kubernetes. This allows users to write "500m" for CPU,
// and "50Mi" for memory (etc.)
func limits(cpu, memory string) (api.ResourceList, error) {
	var rCPU, rMem *resource.Quantity
	var err error

	parse := func(s string) (*resource.Quantity, error) {
		var q *resource.Quantity
		if len(s) == 0 {
			return q, nil
		}
		if q, err = resource.ParseQuantity(s); err != nil {
			return nil, fmt.Errorf("error parsing resource limit: %s", err.Error())
		}
		return q, nil
	}

	if rCPU, err = parse(cpu); err != nil {
		return api.ResourceList{}, nil
	}

	if rMem, err = parse(memory); err != nil {
		return api.ResourceList{}, nil
	}

	l := make(api.ResourceList)

	if rCPU != nil {
		l[api.ResourceLimitsCPU] = *rCPU
	}
	if rMem != nil {
		l[api.ResourceLimitsMemory] = *rMem
	}

	return l, nil
}

// buildVariables converts a common.BuildVariables into a list of
// kubernetes EnvVar objects
func buildVariables(bv common.BuildVariables) []api.EnvVar {
	e := make([]api.EnvVar, len(bv))
	for i, b := range bv {
		e[i] = api.EnvVar{
			Name:  b.Key,
			Value: b.Value,
		}
	}
	return e
}
