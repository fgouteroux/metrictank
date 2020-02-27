package main

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"time"

	"github.com/grafana/metrictank/schema"
)

func produceTestMetrics(schemas []*schema.MetricData) {
	for tick := range time.NewTicker(testMetricsInterval).C {
		for _, metric := range schemas {
			metric.Time = tick.Unix()
			metric.Value = float64(tick.Unix())
		}
		publisher.Flush(schemas)
		log.Infof("flushed schemas for ts %d", tick.Unix())
	}
}

//generateSchemas generates a MetricData that hashes to each of numPartitions partitions
func generateSchemas(numPartitions int32) []*schema.MetricData {
	var metrics []*schema.MetricData
	for i := int32(0); i < numPartitions; i++ {
		metrics = append(metrics, generateSchema(i))
	}
	return metrics
}

//generateSchema generates a single MetricData that hashes to the given partition
func generateSchema(desiredPartition int32) *schema.MetricData {
	metric := schema.MetricData{
		OrgId:    orgId,
		Unit:     "partyparrots",
		Mtype:    "gauge",
		Interval: int(testMetricsInterval.Seconds()),
	}

	for i := 1; true; i++ {
		metric.Name = fmt.Sprintf("parrot.testdata.%d.generated.%s", desiredPartition, generatePartitionSuffix(i))
		id, err := metric.PartitionID(partitionMethod, partitionCount)
		if err != nil {
			log.Fatal(err)
		}
		if id == desiredPartition {
			log.Infof("metric for partition %d: %s", desiredPartition, metric.Name)
			return &metric
		}
	}
	return nil
}

var alphabet = []rune("abcdefghijklmnopqrstuvwxyz")

//generatePartitionSuffix deterministically generates a suffix for partition by brute force
func generatePartitionSuffix(i int) string {
	if i > 25 {
		return generatePartitionSuffix((i/26)-1) + string(alphabet[i%26])
	}
	return string(alphabet[i%26])
}
