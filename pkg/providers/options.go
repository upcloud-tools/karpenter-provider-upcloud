package providers

import (
	"fmt"
	"os"
	"time"
)

const defaultRepairToleration = 30 * time.Minute

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

	repairToleration := defaultRepairToleration
	if v := os.Getenv("UPCLOUD_REPAIR_TOLERATION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("parsing UPCLOUD_REPAIR_TOLERATION %q: %w", v, err)
		}
		repairToleration = d
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
