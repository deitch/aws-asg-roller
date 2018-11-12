package main

import (
	"fmt"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func kubeGetClientset() (*kubernetes.Clientset, error) {
	useKube := os.Getenv("ROLLER_KUBERNETES") == "true"
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		if err == rest.ErrNotInCluster {
			if !useKube {
				return nil, nil
			}
			config, err = getKubeOutOfCluster()
			if err != nil {
				return nil, err
			}
		} else {
			return nil, fmt.Errorf("Error getting kubernetes config from within cluster")
		}
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return clientset, nil
}
func getKubeOutOfCluster() (*rest.Config, error) {
	var kubeconfig string
	kubeconfig = os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		if home := homeDir(); home != "" {
			kubeconfig = filepath.Join(home, ".kube", "config")
		} else {
			return nil, fmt.Errorf("Not KUBECONFIG provided and no home available")
		}
	}

	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err.Error())
	}
	return config, nil
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}

func kubeGetUnreadyCount(clientset *kubernetes.Clientset, hostnames []string) (int, error) {
	hostHash := map[string]bool{}
	for _, h := range hostnames {
		hostHash[h] = true
	}
	/*
		in AWS, the `name` of the node *always* is the internal private DNS name
		you can get a node by name by doing Nodes().Get(name)
		In other words the `name` of the node is set independently and does not care what
		the kubelet had for --hostname-override.
		However, if you want multiple nodes, you need to use the `List()` interface.
		This interface does not accept multiple hostnames. It lists everything, subject only to a filter
		The filter, however, can filter only on labels, and not on the name.
		We _should_ be able to just filter on kubernetes.io/hostname label, but this label *does*
		respect --hostname-override, which we do not know if it is set or not. Oops.
		This, for now, we are stuck doing multiple Get(), one for each hostname, or doing a List() of all nodes
	*/
	nodes, err := clientset.CoreV1().Nodes().List(v1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("Unexpected error getting nodes for cluster: %v", err)
	}
	unReadyCount := 0
	for _, n := range nodes.Items {
		// first make sure that this is one of the new nodes we care about
		if _, ok := hostHash[n.ObjectMeta.Name]; !ok {
			continue
		}
		// next check its status
		conditions := n.Status.Conditions
		if conditions[len(conditions)-1].Type != corev1.NodeReady {
			unReadyCount++
		}
	}
	return unReadyCount, nil
}
