package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	drain "github.com/openshift/kubernetes-drain"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const clusterAutoscalerScaleDownDisabledFlag = "cluster-autoscaler.kubernetes.io/scale-down-disabled"

type kubernetesReadiness struct {
	clientset        *kubernetes.Clientset
	ignoreDaemonSets bool
	deleteLocalData  bool
}

func (k *kubernetesReadiness) getUnreadyCount(hostnames []string, ids []string) (int, error) {
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
	nodes, err := k.clientset.CoreV1().Nodes().List(v1.ListOptions{})
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
func (k *kubernetesReadiness) prepareTermination(hostnames []string, ids []string) error {
	// get the node reference - first need the hostname
	var (
		node *corev1.Node
		err  error
	)
	for _, h := range hostnames {
		node, err = k.clientset.CoreV1().Nodes().Get(h, v1.GetOptions{})
		if err != nil {
			return fmt.Errorf("Unexpected error getting kubernetes node %s: %v", h, err)
		}
		// set options and drain nodes
		err = drain.Drain(k.clientset, []*corev1.Node{node}, &drain.DrainOptions{
			IgnoreDaemonsets:   k.ignoreDaemonSets,
			GracePeriodSeconds: -1,
			Force:              true,
			DeleteLocalData:    k.deleteLocalData,
		})
		if err != nil {
			return fmt.Errorf("Unexpected error draining kubernetes node %s: %v", h, err)
		}
	}
	return nil
}

func kubeGetClientset() (*kubernetes.Clientset, error) {
	envValue := os.Getenv("ROLLER_KUBERNETES")
	// if it is *explicitly* set to false, then do nothing
	if envValue == "false" {
		return nil, nil
	}
	// if it is not explicitly set to false, then it depends on if we are in a cluster or not
	useKube := envValue == "true"
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
	kubeconfig := os.Getenv("KUBECONFIG")
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

func kubeGetReadinessHandler(ignoreDaemonSets bool, deleteLocalData bool) (readiness, error) {
	clientset, err := kubeGetClientset()
	if err != nil {
		log.Fatalf("Error getting kubernetes connection: %v", err)
	}
	if clientset == nil {
		return nil, nil
	}
	return &kubernetesReadiness{clientset: clientset, ignoreDaemonSets: ignoreDaemonSets, deleteLocalData: deleteLocalData}, nil
}

// setScaleDownDisabledAnnotation set the "cluster-autoscaler.kubernetes.io/scale-down-disabled" annotation
// on the list of nodes if required. Returns a list of 151 where the annotation
// is applied.
func setScaleDownDisabledAnnotation(hostnames []string) ([]string, error) {
	// get the node reference - first need the hostname
	var (
		node      *corev1.Node
		err       error
		key       = clusterAutoscalerScaleDownDisabledFlag
		annotated = []string{}
	)
	clientset, err := kubeGetClientset()
	if err != nil {
		log.Fatalf("Error getting kubernetes connection: %v", err)
	}
	if clientset == nil {
		return annotated, nil
	}
	nodes := clientset.CoreV1().Nodes()
	for _, h := range hostnames {
		node, err = nodes.Get(h, v1.GetOptions{})
		if err != nil {
			return annotated, fmt.Errorf("Unexpected error getting kubernetes node %s: %v", h, err)
		}
		annotations := node.GetAnnotations()
		if value := annotations[key]; value != "true" {
			annotations[key] = "true"
			node.SetAnnotations(annotations)
			_, err := nodes.Update(node)
			if err != nil {
				return annotated, err
			}
			annotated = append(annotated, h)
		}
	}
	return annotated, nil
}
func removeScaleDownDisabledAnnotation(hostnames []string) error {
	// get the node reference - first need the hostname
	var (
		node *corev1.Node
		err  error
		key  = clusterAutoscalerScaleDownDisabledFlag
	)
	clientset, err := kubeGetClientset()
	if err != nil {
		log.Fatalf("Error getting kubernetes connection: %v", err)
	}
	if clientset == nil {
		return nil
	}
	nodes := clientset.CoreV1().Nodes()
	for _, h := range hostnames {
		node, err = nodes.Get(h, v1.GetOptions{})
		if err != nil {
			return fmt.Errorf("Unexpected error getting kubernetes node %s: %v", h, err)
		}
		annotations := node.GetAnnotations()
		if _, ok := annotations[key]; ok {
			delete(annotations, key)
			node.SetAnnotations(annotations)
			_, err := nodes.Update(node)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
