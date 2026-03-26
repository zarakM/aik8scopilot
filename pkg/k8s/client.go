package k8s

// client-go is the official Go library for talking to the Kubernetes API.
// It's what kubectl itself uses under the hood.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Client wraps the kubernetes clientset.
// The struct holds state (the clientset) and methods act on it.
// In Go, this is how you do "classes" — a struct + methods with a receiver.
type Client struct {
	clientset *kubernetes.Clientset
	serverURL string // API server URL, used to generate an anonymous cluster fingerprint
}

// ServerURL returns the cluster API server URL for anonymization purposes.
func (c *Client) ServerURL() string {
	return c.serverURL
}

// DiagnosticData is everything we feed to the AI.
// Struct tags (json:"...") control how fields serialize to JSON — not needed
// here but good habit for types that might be logged or serialized later.
type DiagnosticData struct {
	PodName      string
	Namespace    string
	PodSpec      string // Formatted summary of the pod spec
	Logs         string // Last N lines from all containers
	Events       string // Kubernetes events for this pod
	LogLineCount int
	EventCount   int
	Containers   []ContainerStatus
}

// ContainerStatus summarises the runtime state of a single container.
type ContainerStatus struct {
	Name         string
	State        string // Running / Waiting: <reason> / Terminated: <reason>
	RestartCount int32
	LastState    string // Crash details from the previous container instance
	Ready        bool
}

// NewClient builds a Kubernetes client from your kubeconfig.
// If kubeconfigPath is empty it falls back to ~/.kube/config (the default).
func NewClient(kubeconfigPath string) (*Client, error) {
	if kubeconfigPath == "" {
		home, _ := os.UserHomeDir()
		kubeconfigPath = filepath.Join(home, ".kube", "config")
	}

	// BuildConfigFromFlags reads the kubeconfig and returns a *rest.Config
	// that the clientset uses to make API calls.
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("could not load kubeconfig from %s: %w", kubeconfigPath, err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("could not create kubernetes client: %w", err)
	}

	return &Client{clientset: clientset, serverURL: config.Host}, nil
}

// GatherDiagnostics is the main data collection function.
// It fetches the pod, its logs, and its events — everything the AI needs.
func (c *Client) GatherDiagnostics(ctx context.Context, namespace, podName string, logLines int) (*DiagnosticData, error) {
	// We return a pointer to DiagnosticData (*DiagnosticData) rather than a copy.
	// Pointers avoid copying large structs and allow nil to signal "not found".
	data := &DiagnosticData{
		PodName:   podName,
		Namespace: namespace,
	}

	// --- 1. Fetch the pod object ---
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("pod %q not found in namespace %q: %w", podName, namespace, err)
	}

	data.PodSpec = formatPodSpec(pod)

	// --- 2. Parse container statuses ---
	// pod.Status.ContainerStatuses is a slice — we range over it.
	// Range gives us index (i) and value (cs). We discard i with _.
	for _, cs := range pod.Status.ContainerStatuses {
		status := ContainerStatus{
			Name:         cs.Name,
			RestartCount: cs.RestartCount,
			Ready:        cs.Ready,
		}

		// Only one of Running/Waiting/Terminated will be non-nil at a time.
		switch {
		case cs.State.Running != nil:
			status.State = "Running"
		case cs.State.Waiting != nil:
			status.State = fmt.Sprintf("Waiting: %s — %s",
				cs.State.Waiting.Reason, cs.State.Waiting.Message)
		case cs.State.Terminated != nil:
			status.State = fmt.Sprintf("Terminated: %s (exit code %d)",
				cs.State.Terminated.Reason, cs.State.Terminated.ExitCode)
		}

		if cs.LastTerminationState.Terminated != nil {
			lt := cs.LastTerminationState.Terminated
			status.LastState = fmt.Sprintf("Exit code %d (%s)", lt.ExitCode, lt.Reason)
		}

		data.Containers = append(data.Containers, status)
	}

	// --- 3. Fetch logs ---
	// We try current logs AND previous logs (crash logs from before the restart).
	// Previous logs are the most useful for CrashLoopBackOff diagnosis.
	var allLogs []string
	for _, container := range pod.Spec.Containers {
		logs, err := c.fetchLogs(ctx, namespace, podName, container.Name, int64(logLines), false)
		if err == nil && logs != "" {
			allLogs = append(allLogs, fmt.Sprintf("=== %s (current) ===\n%s", container.Name, logs))
		}

		// Previous logs exist if the container has crashed and restarted.
		prevLogs, _ := c.fetchLogs(ctx, namespace, podName, container.Name, int64(logLines), true)
		if prevLogs != "" {
			allLogs = append(allLogs, fmt.Sprintf("=== %s (previous crashed instance) ===\n%s", container.Name, prevLogs))
		}
	}
	data.Logs = strings.Join(allLogs, "\n")
	data.LogLineCount = strings.Count(data.Logs, "\n")

	// --- 4. Fetch events ---
	// FieldSelector filters to only events for this specific pod.
	events, err := c.clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s", podName, namespace),
	})
	if err == nil {
		data.Events = formatEvents(events)
		data.EventCount = len(events.Items)
	}

	return data, nil
}

// fetchLogs gets the last N lines from a container.
// previous=true fetches logs from the crashed/previous instance.
func (c *Client) fetchLogs(ctx context.Context, namespace, podName, containerName string, lines int64, previous bool) (string, error) {
	opts := &corev1.PodLogOptions{
		Container: containerName,
		TailLines: &lines,
		Previous:  previous,
	}

	req := c.clientset.CoreV1().Pods(namespace).GetLogs(podName, opts)
	logBytes, err := req.DoRaw(ctx)
	if err != nil {
		return "", err
	}

	return string(logBytes), nil
}

// PendingDiagnosticData holds everything the AI needs to diagnose a pending pod.
// Pending pods never get logs — the scheduler couldn't place them on a node yet.
// Instead we collect node capacity/taints, resource quotas, and PVC binding state.
type PendingDiagnosticData struct {
	PodName      string
	Namespace    string
	PodSpec      string // Formatted summary of pod spec (requests, tolerations, affinity)
	Events       string // Scheduler + controller events — usually contain the direct reason
	EventCount   int
	NodeSummary  string // Capacity, allocatable, taints, and conditions for every node
	QuotaSummary string // ResourceQuotas in the namespace (quota exhaustion blocks scheduling)
	PVCSummary   string // PVC binding state for any volumes the pod references
}

// GatherPendingDiagnostics collects scheduling context for a pod stuck in Pending.
func (c *Client) GatherPendingDiagnostics(ctx context.Context, namespace, podName string) (*PendingDiagnosticData, error) {
	data := &PendingDiagnosticData{
		PodName:   podName,
		Namespace: namespace,
	}

	// --- 1. Fetch the pod ---
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("pod %q not found in namespace %q: %w", podName, namespace, err)
	}
	data.PodSpec = formatPodSpec(pod)

	// --- 2. Fetch events ---
	// For pending pods these are the most valuable signal — the scheduler writes
	// "0/3 nodes available: 1 insufficient memory, 2 had taints that the pod didn't tolerate."
	events, err := c.clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s", podName, namespace),
	})
	if err == nil {
		data.Events = formatEvents(events)
		data.EventCount = len(events.Items)
	}

	// --- 3. Summarise all nodes ---
	// The scheduler needs a node with enough allocatable CPU/memory and no blocking taints.
	nodes, err := c.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err == nil {
		data.NodeSummary = formatNodes(nodes)
	}

	// --- 4. Fetch ResourceQuotas in the namespace ---
	// A namespace quota that's nearly full will silently block new pods.
	quotas, err := c.clientset.CoreV1().ResourceQuotas(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		data.QuotaSummary = formatQuotas(quotas)
	}

	// --- 5. Check PVC binding for volumes the pod references ---
	// An unbound PVC keeps the pod pending indefinitely (no storage = no schedule).
	data.PVCSummary = formatPodPVCs(ctx, c, namespace, pod)

	return data, nil
}

// formatNodes summarises capacity, allocatable resources, taints, and ready condition
// for every node so the AI can see exactly what the scheduler sees.
func formatNodes(nodes *corev1.NodeList) string {
	if len(nodes.Items) == 0 {
		return "No nodes found in cluster."
	}

	var sb strings.Builder
	for _, n := range nodes.Items {
		sb.WriteString(fmt.Sprintf("Node: %s\n", n.Name))

		// Capacity vs allocatable — the scheduler uses allocatable (capacity minus OS/daemon overhead).
		sb.WriteString(fmt.Sprintf("  Capacity:    cpu=%s, memory=%s\n",
			n.Status.Capacity.Cpu().String(),
			n.Status.Capacity.Memory().String()))
		sb.WriteString(fmt.Sprintf("  Allocatable: cpu=%s, memory=%s\n",
			n.Status.Allocatable.Cpu().String(),
			n.Status.Allocatable.Memory().String()))

		// Taints block pods that don't have matching tolerations.
		if len(n.Spec.Taints) > 0 {
			sb.WriteString("  Taints:\n")
			for _, t := range n.Spec.Taints {
				sb.WriteString(fmt.Sprintf("    %s=%s:%s\n", t.Key, t.Value, t.Effect))
			}
		}

		// Node conditions — NotReady nodes can't accept new pods.
		for _, cond := range n.Status.Conditions {
			if cond.Type == corev1.NodeReady {
				sb.WriteString(fmt.Sprintf("  Ready: %s (%s)\n", cond.Status, cond.Reason))
			}
		}

		sb.WriteString("\n")
	}
	return sb.String()
}

// formatQuotas shows used vs hard limit for each ResourceQuota.
// When used >= hard the scheduler rejects new pods in that namespace.
func formatQuotas(quotas *corev1.ResourceQuotaList) string {
	if len(quotas.Items) == 0 {
		return "No ResourceQuotas defined in this namespace."
	}

	var sb strings.Builder
	for _, q := range quotas.Items {
		sb.WriteString(fmt.Sprintf("ResourceQuota: %s\n", q.Name))
		for resource, hard := range q.Status.Hard {
			used := q.Status.Used[resource]
			sb.WriteString(fmt.Sprintf("  %s: used=%s / hard=%s\n",
				resource, used.String(), hard.String()))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// formatPodPVCs fetches and formats binding status for each PVC the pod volumes reference.
// Returns a summary string; never returns an error — missing PVC info just says "not found".
func formatPodPVCs(ctx context.Context, c *Client, namespace string, pod *corev1.Pod) string {
	var pvcNames []string
	for _, vol := range pod.Spec.Volumes {
		if vol.PersistentVolumeClaim != nil {
			pvcNames = append(pvcNames, vol.PersistentVolumeClaim.ClaimName)
		}
	}

	if len(pvcNames) == 0 {
		return "Pod does not reference any PersistentVolumeClaims."
	}

	var sb strings.Builder
	for _, name := range pvcNames {
		pvc, err := c.clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			sb.WriteString(fmt.Sprintf("PVC %q: not found (%v)\n", name, err))
			continue
		}
		sb.WriteString(fmt.Sprintf("PVC %q: phase=%s, storageClass=%s, capacity=%s\n",
			name,
			pvc.Status.Phase,
			stringOrDefault(pvc.Spec.StorageClassName, "<none>"),
			pvc.Status.Capacity.Storage().String()))
	}
	return sb.String()
}

// stringOrDefault dereferences a *string and returns fallback if nil.
func stringOrDefault(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}

// formatPodSpec builds a human-readable summary of the pod spec.
// strings.Builder is Go's efficient way to build strings in a loop —
// avoids creating a new string object on every concatenation.
func formatPodSpec(pod *corev1.Pod) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Pod: %s/%s\n", pod.Namespace, pod.Name))
	sb.WriteString(fmt.Sprintf("Phase: %s\n", pod.Status.Phase))
	sb.WriteString(fmt.Sprintf("Node: %s\n", pod.Spec.NodeName))
	sb.WriteString(fmt.Sprintf("Labels: %v\n\n", pod.Labels))

	sb.WriteString("Containers:\n")
	for _, c := range pod.Spec.Containers {
		sb.WriteString(fmt.Sprintf("  — %s\n", c.Name))
		sb.WriteString(fmt.Sprintf("    Image: %s\n", c.Image))

		if c.Resources.Requests != nil {
			sb.WriteString(fmt.Sprintf("    Requests: cpu=%s, memory=%s\n",
				c.Resources.Requests.Cpu().String(),
				c.Resources.Requests.Memory().String()))
		}
		if c.Resources.Limits != nil {
			sb.WriteString(fmt.Sprintf("    Limits: cpu=%s, memory=%s\n",
				c.Resources.Limits.Cpu().String(),
				c.Resources.Limits.Memory().String()))
		}

		if c.ReadinessProbe != nil {
			sb.WriteString(fmt.Sprintf("    ReadinessProbe: initialDelay=%ds, period=%ds\n",
				c.ReadinessProbe.InitialDelaySeconds,
				c.ReadinessProbe.PeriodSeconds))
		}

		for _, env := range c.Env {
			if env.ValueFrom == nil {
				sb.WriteString(fmt.Sprintf("    Env: %s=%s\n", env.Name, env.Value))
			} else {
				// Don't print actual secret values — just signal they're present.
				sb.WriteString(fmt.Sprintf("    Env: %s=<from secret/configmap>\n", env.Name))
			}
		}

		// Check for missing envFrom sources — a common cause of crashes.
		for _, envFrom := range c.EnvFrom {
			if envFrom.SecretRef != nil {
				sb.WriteString(fmt.Sprintf("    EnvFrom secret: %s\n", envFrom.SecretRef.Name))
			}
			if envFrom.ConfigMapRef != nil {
				sb.WriteString(fmt.Sprintf("    EnvFrom configmap: %s\n", envFrom.ConfigMapRef.Name))
			}
		}
	}

	return sb.String()
}

func formatEvents(events *corev1.EventList) string {
	if len(events.Items) == 0 {
		return "No events found."
	}

	var sb strings.Builder
	for _, e := range events.Items {
		// Type is "Normal" or "Warning". Warning events are the interesting ones.
		sb.WriteString(fmt.Sprintf("[%s] %s: %s\n", e.Type, e.Reason, e.Message))
	}
	return sb.String()
}
