package siri_vm

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/britbus/britbus/pkg/ctdf"
	"github.com/britbus/britbus/pkg/rabbitmq"
	"github.com/eko/gocache/v2/cache"
	"github.com/rs/zerolog/log"
	"github.com/streadway/amqp"
)

type SiriVM struct {
	ServiceDelivery struct {
		ResponseTimestamp string
		ProducerRef       string

		VehicleMonitoringDelivery struct {
			ResponseTimestamp     string
			RequestMessageRef     string
			ValidUntil            string
			ShortestPossibleCycle string

			VehicleActivity []*VehicleActivity
		}
	}
}

func (s *SiriVM) SubmitToProcessQueue(datasource *ctdf.DataSource, cacheManager *cache.Cache) {
	datasource.OriginalFormat = "siri-vm"
	log.Info().Msgf("Submitting the %d activity records in %s to processing queue", len(s.ServiceDelivery.VehicleMonitoringDelivery.VehicleActivity), s.ServiceDelivery.VehicleMonitoringDelivery.RequestMessageRef)

	responseTime, _ := time.Parse(time.RFC3339, s.ServiceDelivery.ResponseTimestamp)

	channel, err := rabbitmq.GetChannel()
	if err != nil {
		log.Error().Err(err).Msg("Failed to create RabbitMQ Channel")
	}
	queue, err := channel.QueueDeclare(
		"vehicle_location_events",
		true,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create RabbitMQ Queue")
	}
	var wg sync.WaitGroup

	for _, vehicle := range s.ServiceDelivery.VehicleMonitoringDelivery.VehicleActivity {
		wg.Add(1)

		go func(wg *sync.WaitGroup, vehicle *VehicleActivity, cache *cache.Cache) {
			defer wg.Done()

			vehicleJourneyRef := vehicle.MonitoredVehicleJourney.VehicleJourneyRef

			if vehicle.MonitoredVehicleJourney.FramedVehicleJourneyRef.DatedVehicleJourneyRef != "" {
				vehicleJourneyRef = vehicle.MonitoredVehicleJourney.FramedVehicleJourneyRef.DatedVehicleJourneyRef
			}

			localJourneyID := fmt.Sprintf(
				"%s:%s:%s:%s",
				fmt.Sprintf(ctdf.OperatorNOCFormat, vehicle.MonitoredVehicleJourney.OperatorRef),
				vehicle.MonitoredVehicleJourney.LineRef,
				fmt.Sprintf(ctdf.StopIDFormat, vehicle.MonitoredVehicleJourney.OriginRef),
				vehicleJourneyRef,
			)

			var journeyID string

			cachedJourneyMapping, _ := cache.Get(context.Background(), localJourneyID)

			if cachedJourneyMapping == nil {
				journey, err := ctdf.IdentifyJourney(map[string]string{
					"ServiceNameRef":           vehicle.MonitoredVehicleJourney.LineRef,
					"DirectionRef":             vehicle.MonitoredVehicleJourney.DirectionRef,
					"PublishedLineName":        vehicle.MonitoredVehicleJourney.PublishedLineName,
					"OperatorRef":              fmt.Sprintf(ctdf.OperatorNOCFormat, vehicle.MonitoredVehicleJourney.OperatorRef),
					"VehicleJourneyRef":        vehicleJourneyRef,
					"OriginRef":                fmt.Sprintf(ctdf.StopIDFormat, vehicle.MonitoredVehicleJourney.OriginRef),
					"DestinationRef":           fmt.Sprintf(ctdf.StopIDFormat, vehicle.MonitoredVehicleJourney.DestinationRef),
					"OriginAimedDepartureTime": vehicle.MonitoredVehicleJourney.OriginAimedDepartureTime,
					"FramedVehicleJourneyDate": vehicle.MonitoredVehicleJourney.FramedVehicleJourneyRef.DataFrameRef,
				})

				if err != nil {
					// log.Error().Err(err).Str("localjourneyid", localJourneyID).Msgf("Could not find Journey")
					return
				}
				journeyID = journey.PrimaryIdentifier

				cache.Set(context.Background(), localJourneyID, journeyID, nil)
			} else {
				journeyID = cachedJourneyMapping.(string)
			}

			// pretty.Println(localJourneyID, journeyID)

			locationEventJSON, _ := json.Marshal(ctdf.VehicleLocationEvent{
				JourneyRef:       journeyID,
				CreationDateTime: responseTime,

				DataSource: datasource,

				VehicleLocation: ctdf.Location{
					Type: "Point",
					Coordinates: []float64{
						vehicle.MonitoredVehicleJourney.VehicleLocation.Longitude,
						vehicle.MonitoredVehicleJourney.VehicleLocation.Latitude,
					},
				},
				VehicleBearing: vehicle.MonitoredVehicleJourney.Bearing,
			})
			err = channel.Publish(
				"",         // exchange
				queue.Name, // routing key
				false,      // mandatory
				false,
				amqp.Publishing{
					DeliveryMode: amqp.Persistent,
					ContentType:  "text/plain",
					Body:         []byte(locationEventJSON),
				})
		}(&wg, vehicle, cacheManager)
	}

	wg.Wait()
}