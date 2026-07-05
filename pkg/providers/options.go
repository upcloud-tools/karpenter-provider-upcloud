package providers

import (
	"fmt"
	"os"
)

type Options struct {
	Token       string
	ClusterUUID string
}

func NewOptionsFromEnvironment() (*Options, error) {
	token, err := getRequiredEnv("UPCLOUD_TOKEN")
	if err != nil {
		return nil, err
	}

	clusterUUID, err := getRequiredEnv("UPCLOUD_KUBERNETES_CLUSTER_ID")
	if err != nil {
		return nil, err
	}

	return &Options{
		Token:       token,
		ClusterUUID: clusterUUID,
	}, nil
}

func getRequiredEnv(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("%s environment variable is required", key)
	}
	return value, nil
}
