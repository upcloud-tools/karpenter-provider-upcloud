package providers

import (
	"fmt"
	"os"
	"time"
)

type Options struct {
	Token            string
	ClusterUUID      string
	RepairToleration time.Duration
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

	repairTolerationStr, err := getRequiredEnv("UPCLOUD_REPAIR_TOLERATION")
	if err != nil {
		return nil, err
	}
	repairToleration, err := time.ParseDuration(repairTolerationStr)
	if err != nil {
		return nil, fmt.Errorf("parsing UPCLOUD_REPAIR_TOLERATION %q: %w", repairTolerationStr, err)
	}

	return &Options{
		Token:            token,
		ClusterUUID:      clusterUUID,
		RepairToleration: repairToleration,
	}, nil
}

func getRequiredEnv(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("%s environment variable is required", key)
	}
	return value, nil
}
