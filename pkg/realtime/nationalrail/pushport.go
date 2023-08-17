package nationalrail

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/travigo/travigo/pkg/ctdf"
	"github.com/travigo/travigo/pkg/database"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type PushPortData struct {
	TrainStatuses []TrainStatus
}

func (p *PushPortData) UpdateRealtimeJourneys() {
	now := time.Now()
	datasource := &ctdf.DataSource{
		OriginalFormat: "DarwinPushPort",
		Provider:       "National-Rail",
		Dataset:        "DarwinPushPort",
		Identifier:     now.String(),
	}

	realtimeJourneysCollection := database.GetCollection("realtime_journeys")
	journeysCollection := database.GetCollection("journeys")

	operations := []mongo.WriteModel{}

	for _, trainStatus := range p.TrainStatuses {
		realtimeJourneyID := fmt.Sprintf("GB:DARWIN:%s:%s", trainStatus.SSD, trainStatus.UID)
		searchQuery := bson.M{"primaryidentifier": realtimeJourneyID}

		var realtimeJourney *ctdf.RealtimeJourney

		realtimeJourneysCollection.FindOne(context.Background(), searchQuery).Decode(&realtimeJourney)

		newRealtimeJourney := false
		if realtimeJourney == nil {
			// Find the journey for this train
			var journey *ctdf.Journey
			cursor, _ := journeysCollection.Find(context.Background(), bson.M{"otheridentifiers.TrainUID": trainStatus.UID})

			journeyDate, _ := time.Parse("2006-01-02", trainStatus.SSD)

			for cursor.Next(context.TODO()) {
				var potentialJourney *ctdf.Journey
				err := cursor.Decode(&potentialJourney)
				if err != nil {
					log.Error().Err(err).Msg("Failed to decode Journey")
				}

				if potentialJourney.Availability.MatchDate(journeyDate) {
					journey = potentialJourney
				}
			}

			if journey == nil {
				log.Error().Str("uid", trainStatus.UID).Msg("Failed to find respective Journey for this train")
				continue
			}

			// Construct the base realtime journey
			realtimeJourney = &ctdf.RealtimeJourney{
				PrimaryIdentifier: realtimeJourneyID,
				ActivelyTracked:   false,
				CreationDateTime:  now,
				Reliability:       ctdf.RealtimeJourneyReliabilityExternalProvided,

				DataSource: datasource,

				Journey: journey,

				Stops: map[string]*ctdf.RealtimeJourneyStops{},
			}

			newRealtimeJourney = true
		}

		updateMap := bson.M{
			"modificationdatetime": now,
		}

		// Update database
		if newRealtimeJourney {
			updateMap["primaryidentifier"] = realtimeJourney.PrimaryIdentifier
			updateMap["activelytracked"] = realtimeJourney.ActivelyTracked

			updateMap["reliability"] = realtimeJourney.Reliability

			updateMap["creationdatetime"] = realtimeJourney.CreationDateTime
			updateMap["datasource"] = realtimeJourney.DataSource

			updateMap["journey"] = realtimeJourney.Journey
		} else {
			updateMap["datasource.identifier"] = datasource.Identifier
		}

		for _, location := range trainStatus.Locations {
			stop := getStopFromTiploc(location.TPL)

			if stop == nil {
				log.Error().Str("tiploc", location.TPL).Msg("Failed to find stop")
				continue
			}

			journeyStop := realtimeJourney.Stops[stop.PrimaryIdentifier]
			journeyStopUpdated := false

			if realtimeJourney.Stops[stop.PrimaryIdentifier] == nil {
				journeyStop = &ctdf.RealtimeJourneyStops{
					StopRef:  stop.PrimaryIdentifier,
					TimeType: ctdf.RealtimeJourneyStopTimeEstimatedFuture,
				}
			}

			if location.Arrival != nil {
				arrivalTime, _ := time.Parse("15:04", location.Arrival.ET)

				journeyStop.ArrivalTime = arrivalTime
				journeyStopUpdated = true
			}

			if location.Departure != nil {
				departureTime, _ := time.Parse("15:04", location.Departure.ET)

				journeyStop.DepartureTime = departureTime
				journeyStopUpdated = true
			}

			if journeyStopUpdated {
				updateMap[fmt.Sprintf("stops.%s", stop.PrimaryIdentifier)] = journeyStop
			}
		}

		if trainStatus.LateReason != "" {
			updateMap["annotations.LateReason"] = trainStatus.LateReason
		}

		// Create update
		bsonRep, _ := bson.Marshal(bson.M{"$set": updateMap})
		updateModel := mongo.NewUpdateOneModel()
		updateModel.SetFilter(searchQuery)
		updateModel.SetUpdate(bsonRep)
		updateModel.SetUpsert(true)

		operations = append(operations, updateModel)
	}

	if len(operations) > 0 {
		_, err := realtimeJourneysCollection.BulkWrite(context.TODO(), operations, &options.BulkWriteOptions{})
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to bulk write Journeys")
		}
	}
}

// TODO convert to proper cache
var tiplocStopCacheMutex sync.Mutex
var tiplocStopCache map[string]*ctdf.Stop

func getStopFromTiploc(tiploc string) *ctdf.Stop {
	tiplocStopCacheMutex.Lock()
	cacheValue := tiplocStopCache[tiploc]
	tiplocStopCacheMutex.Unlock()

	if cacheValue != nil {
		return cacheValue
	}

	stopCollection := database.GetCollection("stops")
	var stop *ctdf.Stop
	stopCollection.FindOne(context.Background(), bson.M{"otheridentifiers.Tiploc": tiploc}).Decode(&stop)

	tiplocStopCacheMutex.Lock()
	tiplocStopCache[tiploc] = stop
	tiplocStopCacheMutex.Unlock()

	return stop
}