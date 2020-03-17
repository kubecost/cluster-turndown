package provider

import (
	"net/http"

	"cloud.google.com/go/compute/metadata"

	"k8s.io/klog"
)

const (
	KubecostTurndownUserAgent = "cluster-turndown"
	GKEMetaDataProjectIDKey   = "projectid"
	GKEMetaDataZoneKey        = "zone"
	GKEMetaDataClusterNameKey = "cluster-name"
)

type GKEMetaData struct {
	client *metadata.Client
	cache  map[string]string
}

func NewGKEMetaData() *GKEMetaData {
	c := metadata.NewClient(&http.Client{
		Transport: UserAgentTransport{
			userAgent: KubecostTurndownUserAgent,
			base:      http.DefaultTransport,
		},
	})

	return &GKEMetaData{
		client: c,
		cache:  make(map[string]string),
	}
}

func (md *GKEMetaData) GetProjectID() string {
	pid, ok := md.cache[GKEMetaDataProjectIDKey]
	if ok {
		return pid
	}

	projectID, err := md.client.ProjectID()
	if err != nil {
		klog.V(1).Infof("[Error] %s", err.Error())
		return ""
	}

	md.cache[GKEMetaDataProjectIDKey] = projectID
	return projectID
}

func (md *GKEMetaData) GetClusterID() string {
	cn, ok := md.cache[GKEMetaDataClusterNameKey]
	if ok {
		return cn
	}

	attribute, err := md.client.InstanceAttributeValue("cluster-name")
	if err != nil {
		klog.V(1).Infof("[Error] %s", err.Error())
		return ""
	}

	md.cache[GKEMetaDataClusterNameKey] = attribute
	return attribute
}

func (md *GKEMetaData) GetZone() string {
	z, ok := md.cache[GKEMetaDataZoneKey]
	if ok {
		return z
	}

	zone, err := md.client.Zone()
	if err != nil {
		klog.V(1).Infof("[Error] %s", err.Error())
		return ""
	}

	md.cache[GKEMetaDataZoneKey] = zone
	return zone
}
