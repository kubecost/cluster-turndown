package provider

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strings"

	"cloud.google.com/go/compute/metadata"

	"k8s.io/klog"
)

const (
	KubecostTurndownUserAgent = "cluster-turndown"
	GKEMetaDataProjectIDKey   = "projectid"
	GKEMetaDataZoneKey        = "zone"
	GKEMetaDataMasterZoneKey  = "master-zone"
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

func (md *GKEMetaData) GetMasterZone() string {
	z, ok := md.cache[GKEMetaDataMasterZoneKey]
	if ok {
		return z
	}

	results, err := md.client.InstanceAttributeValue("kube-env")
	if err != nil {
		klog.V(1).Infof("[Error] %s", err.Error())
		return ""
	}

	ioReader := bufio.NewReader(bytes.NewReader([]byte(results)))
	for {
		line, err := ioReader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				klog.V(1).Infof("Failed to read kube-env data: %s", err.Error())
			}

			return ""
		}

		kv := strings.Split(line, ": ")
		if len(kv) != 2 {
			continue
		}

		if kv[0] == "ZONE" {
			masterZone := strings.TrimSpace(kv[1])
			md.cache[GKEMetaDataMasterZoneKey] = masterZone
			return masterZone
		}
	}
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
