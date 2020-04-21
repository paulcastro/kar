package sidecar

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"strconv"

	"github.ibm.com/solsa/kar.git/pkg/logger"
	v1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var (
	sidecarImage    string
	sidecarImageTag string
)

const (
	actorAnnotation       = "kar.ibm.com/actors"
	appNameAnnotation     = "kar.ibm.com/app"
	serviceNameAnnotation = "kar.ibm.com/service"
	sendPortAnnotation    = "kar.ibm.com/sendPort"
	recvPortAnnotation    = "kar.ibm.com/recvPort"
	verboseAnnotation     = "kar.ibm.com/verbose"

	defaultSendPort = "8080"
	defaultRecvPort = "3500"

	sidecarName        = "kar"
	karImagePullSecret = "kar.ibm.com.image-pull"
	karRTConfigSecret  = "kar.ibm.com.runtime-config"
	karRTConfigMount   = "/var/run/secrets/kar.ibm.com"
)

func init() {
	var ignored string
	flag.StringVar(&ignored, "sidecar_image_registry", "", "DEPRECATED: WILL BE REMOVED")
	flag.StringVar(&sidecarImage, "sidecar_image", "us.icr.io/kar-dev/kar", "docker image to use for kar sidecar")
	flag.StringVar(&sidecarImageTag, "sidecar_image_tag", "latest", "docker image tag to use for kar sidecar")
}

// HandleAdmissionRequest decodes and processes the body of an AdmissionRequest.
// If the processing is successful, it returns an AdmissionReview instance and http.StatusOK.
// If the processing is unsuccessful, it returns an http error code and error.
func HandleAdmissionRequest(body []byte) (runtime.Object, int, error) {
	deserializer := codecs.UniversalDeserializer()
	obj, gvk, err := deserializer.Decode(body, nil, nil)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("Request could not be decoded: %w", err)
	}

	var responseObj runtime.Object
	switch *gvk {
	case v1.SchemeGroupVersion.WithKind("AdmissionReview"):
		requestedAdmissionReview, ok := obj.(*v1.AdmissionReview)
		if !ok {
			return nil, http.StatusBadRequest, fmt.Errorf("Expected v1.AdmissionReview but got: %T", obj)
		}
		responseAdmissionReview := &v1.AdmissionReview{}
		responseAdmissionReview.SetGroupVersionKind(*gvk)
		responseAdmissionReview.Response = possiblyInjectSidecar(*requestedAdmissionReview)
		responseAdmissionReview.Response.UID = requestedAdmissionReview.Request.UID
		responseObj = responseAdmissionReview
	default:
		return nil, http.StatusBadRequest, fmt.Errorf("Unsupported group version kind: %v", gvk)
	}

	return responseObj, http.StatusOK, nil
}

func possiblyInjectSidecar(ar v1.AdmissionReview) *v1.AdmissionResponse {
	podResource := metav1.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	if ar.Request.Resource != podResource {
		logger.Error("expected resource to be %s", podResource)
		return nil
	}

	raw := ar.Request.Object.Raw
	pod := corev1.Pod{}
	deserializer := codecs.UniversalDeserializer()
	if _, _, err := deserializer.Decode(raw, nil, &pod); err != nil {
		logger.Error(err.Error())
		return toV1AdmissionResponse(err)
	}

	reviewResponse := v1.AdmissionResponse{}
	reviewResponse.Allowed = true

	annotations := pod.GetObjectMeta().GetAnnotations()
	if appName, ok := annotations[appNameAnnotation]; ok {
		logger.Info("Pod %v has appName %v", pod.Name, appName)
		containers := pod.Spec.Containers

		for _, container := range containers {
			if container.Name == sidecarName {
				logger.Info("Pod %v already has a container named %v; short-circuiting injection", pod.Name, sidecarName)
				return &reviewResponse
			}
		}

		cmdLine, appEnv, karPortStr := processAnnotations(pod)
		karPort, err := strconv.Atoi(karPortStr)

		if len(appEnv) > 0 {
			for index, container := range containers {
				containers[index].Env = append(container.Env, appEnv...)
			}
		}

		sidecar := []corev1.Container{{
			Name:          sidecarName,
			Image:         fmt.Sprintf("%s:%s", sidecarImage, sidecarImageTag),
			Command:       []string{"/kar/kar"},
			Args:          cmdLine,
			Env:           []corev1.EnvVar{{Name: "KAR_POD_IP", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"}}}},
			Ports:         []corev1.ContainerPort{{ContainerPort: int32(karPort), Protocol: corev1.ProtocolTCP, Name: "kar"}},
			LivenessProbe: &corev1.Probe{Handler: corev1.Handler{HTTPGet: &corev1.HTTPGetAction{Path: "kar/v1/system/health", Port: intstr.FromInt(karPort)}}},
			Lifecycle:     &corev1.Lifecycle{PreStop: &corev1.Handler{HTTPGet: &corev1.HTTPGetAction{Path: "kar/v1/system/kill", Port: intstr.FromInt(karPort)}}},
			VolumeMounts:  []corev1.VolumeMount{{Name: "kar-ibm-com-config", MountPath: karRTConfigMount, ReadOnly: true}},
		}}
		containers = append(sidecar, containers...)
		updateContainersPatch := patchOperation{
			Op:    "replace",
			Path:  "/spec/containers",
			Value: containers,
		}

		configVolume := corev1.Volume{
			Name:         "kar-ibm-com-config",
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: karRTConfigSecret}},
		}
		var addVolumePatch patchOperation
		if pod.Spec.Volumes == nil {
			addVolumePatch = patchOperation{
				Op:    "replace",
				Path:  "/spec/volumes",
				Value: []corev1.Volume{configVolume},
			}
		} else {
			addVolumePatch = patchOperation{
				Op:    "add",
				Path:  "/spec/volumes/-",
				Value: configVolume,
			}
		}

		imagePull := corev1.LocalObjectReference{Name: karImagePullSecret}
		var pullSecretPatch patchOperation
		if pod.Spec.ImagePullSecrets == nil {
			pullSecretPatch = patchOperation{
				Op:    "add",
				Path:  "/spec/imagePullSecrets",
				Value: []corev1.LocalObjectReference{imagePull},
			}
		} else {
			pullSecretPatch = patchOperation{
				Op:    "add",
				Path:  "/spec/imagePullSecrets/-",
				Value: imagePull,
			}
		}

		patches := []patchOperation{updateContainersPatch, addVolumePatch, pullSecretPatch}

		patchBytes, err := json.Marshal(patches)
		if err != nil {
			logger.Error(err.Error())
			return toV1AdmissionResponse(err)
		}

		reviewResponse.Patch = patchBytes
		pt := v1.PatchTypeJSONPatch
		reviewResponse.PatchType = &pt
	} else {
		logger.Info("Pod %v lacks 'kar.ibm.com/app' annotation; no sidecar injected", pod.Name)
	}

	return &reviewResponse
}

func processAnnotations(pod corev1.Pod) ([]string, []corev1.EnvVar, string) {
	annotations := pod.GetObjectMeta().GetAnnotations()
	appName := annotations[appNameAnnotation]
	cmd := []string{"-kubernetes_mode", "-config_dir", karRTConfigMount, "-app", appName}
	appEnv := []corev1.EnvVar{}

	if serviceName, ok := annotations[serviceNameAnnotation]; ok {
		cmd = append(cmd, "-service", serviceName)
	}

	if actors, ok := annotations[actorAnnotation]; ok {
		cmd = append(cmd, "-actors", actors)
	}

	var sendPort = defaultSendPort
	if sp, ok := annotations[sendPortAnnotation]; ok {
		sendPort = sp
	}
	cmd = append(cmd, "-send", sendPort)
	appEnv = append(appEnv, corev1.EnvVar{Name: "KAR_APP_PORT", Value: sendPort})

	var recvPort = defaultRecvPort
	if rp, ok := annotations[recvPortAnnotation]; ok {
		recvPort = rp
	}
	cmd = append(cmd, "-recv", recvPort)
	appEnv = append(appEnv, corev1.EnvVar{Name: "KAR_PORT", Value: recvPort})

	if verbose, ok := annotations[verboseAnnotation]; ok {
		cmd = append(cmd, "-v", verbose)
	}

	return cmd, appEnv, recvPort
}

func toV1AdmissionResponse(err error) *v1.AdmissionResponse {
	return &v1.AdmissionResponse{
		Result: &metav1.Status{
			Message: err.Error(),
		},
	}
}
