package siri_vm

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"time"

	"github.com/adjust/rmq/v5"
	"github.com/rs/zerolog/log"
	"github.com/travigo/travigo/pkg/ctdf"
	"github.com/travigo/travigo/pkg/elastic_client"
	"github.com/travigo/travigo/pkg/redis_client"
	"golang.org/x/net/html/charset"
)

type SiriVMVehicleIdentificationEvent struct {
	VehicleActivity *VehicleActivity
	DataSource      *ctdf.DataSource
	ResponseTime    time.Time
}

type queueEmptyElasticEvent struct {
	Timestamp time.Time
	Duration  int
}

func SubmitToProcessQueue(queue rmq.Queue, vehicle *VehicleActivity, datasource *ctdf.DataSource) bool {
	datasource.OriginalFormat = "siri-vm"

	currentTime := time.Now()

	recordedAtTime, err := time.Parse(ctdf.XSDDateTimeFormat, vehicle.RecordedAtTime)

	if err == nil {
		recordedAtDifference := currentTime.Sub(recordedAtTime)

		// Skip any records that haven't been updated in over 20 minutes
		if recordedAtDifference.Minutes() > 20 {
			return false
		}
	}

	identificationEvent := &SiriVMVehicleIdentificationEvent{
		VehicleActivity: vehicle,
		ResponseTime:    recordedAtTime,
		DataSource:      datasource,
	}

	identificationEventJson, _ := json.Marshal(identificationEvent)

	queue.PublishBytes(identificationEventJson)

	return true
}

func ParseXMLFile(reader io.Reader, queue rmq.Queue, datasource *ctdf.DataSource) error {
	var retrievedRecords int64
	var submittedRecords int64

	d := xml.NewDecoder(reader)
	d.CharsetReader = charset.NewReaderLabel
	for {
		tok, err := d.Token()
		if tok == nil || err == io.EOF {
			// EOF means we're done.
			break
		} else if err != nil {
			log.Fatal().Msgf("Error decoding token: %s", err)
			return err
		}

		switch ty := tok.(type) {
		case xml.StartElement:
			if ty.Name.Local == "VehicleActivity" {
				var vehicleActivity VehicleActivity

				if err = d.DecodeElement(&vehicleActivity, &ty); err != nil {
					log.Fatal().Msgf("Error decoding item: %s", err)
				} else {
					retrievedRecords += 1

					successfullyPublished := SubmitToProcessQueue(queue, &vehicleActivity, datasource)

					if successfullyPublished {
						submittedRecords += 1
					}
				}
			}
		}
	}

	log.Info().Int64("retrieved", retrievedRecords).Int64("submitted", submittedRecords).Msgf("Parsed latest Siri-VM response")

	// Wait for queue to empty
	startTime := time.Now()
	checkQueueSize()
	executionDuration := time.Since(startTime)
	log.Info().Msgf("Queue took %s to empty", executionDuration.String())

	// Publish stats to Elasticsearch
	elasticEvent, _ := json.Marshal(&queueEmptyElasticEvent{
		Duration:  int(executionDuration.Seconds()),
		Timestamp: time.Now(),
	})

	elastic_client.IndexRequest("sirivm-queue-empty-1", bytes.NewReader(elasticEvent))

	return nil
}

func checkQueueSize() {
	stats, _ := redis_client.QueueConnection.CollectStats([]string{"realtime-queue"})
	inQueue := stats.QueueStats["realtime-queue"].ReadyCount

	if inQueue != 0 {
		time.Sleep(1 * time.Second)

		checkQueueSize()
	}
}
