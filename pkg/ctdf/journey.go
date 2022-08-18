package ctdf

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/britbus/britbus/pkg/database"
	"github.com/rs/zerolog/log"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const XSDDateTimeFormat = "2006-01-02T15:04:05-07:00"
const XSDDateTimeWithFractionalFormat = "2006-01-02T15:04:05.999999-07:00"

type Journey struct {
	PrimaryIdentifier string            `groups:"basic"`
	OtherIdentifiers  map[string]string `groups:"basic"`

	CreationDateTime     time.Time `groups:"detailed"`
	ModificationDateTime time.Time `groups:"detailed"`

	DataSource *DataSource `groups:"detailed"`

	ServiceRef string   `groups:"internal"`
	Service    *Service `groups:"basic" json:",omitempty" bson:"-"`

	OperatorRef string    `groups:"internal"`
	Operator    *Operator `groups:"basic" json:",omitempty" bson:"-"`

	Direction          string    `groups:"detailed"`
	DepartureTime      time.Time `groups:"basic"`
	DestinationDisplay string    `groups:"basic"`

	Availability *Availability `groups:"internal"`

	Path []*JourneyPathItem `groups:"detailed"`

	RealtimeJourney *RealtimeJourney `groups:"basic" bson:"-"`
}

func (j *Journey) GetReferences() {
	j.GetOperator()
	j.GetService()
}
func (j *Journey) GetOperator() {
	operatorsCollection := database.GetCollection("operators")
	query := bson.M{"$or": bson.A{bson.M{"primaryidentifier": j.OperatorRef}, bson.M{"otheridentifiers": j.OperatorRef}}}
	operatorsCollection.FindOne(context.Background(), query).Decode(&j.Operator)
}
func (j *Journey) GetService() {
	servicesCollection := database.GetCollection("services")
	servicesCollection.FindOne(context.Background(), bson.M{"primaryidentifier": j.ServiceRef}).Decode(&j.Service)
}
func (j *Journey) GetDeepReferences() {
	for _, path := range j.Path {
		path.GetReferences()
	}
}
func (j *Journey) GetRealtimeJourney(timeframe string) {
	realtimeActiveCutoffDate := GetActiveRealtimeJourneyCutOffDate()

	realtimeJourneyIdentifier := fmt.Sprintf(RealtimeJourneyIDFormat, timeframe, j.PrimaryIdentifier)
	realtimeJourneysCollection := database.GetCollection("realtime_journeys")

	var realtimeJourney *RealtimeJourney
	realtimeJourneysCollection.FindOne(context.Background(), bson.M{
		"primaryidentifier":    realtimeJourneyIdentifier,
		"modificationdatetime": bson.M{"$gt": realtimeActiveCutoffDate},
	}).Decode(&realtimeJourney)

	if realtimeJourney != nil && realtimeJourney.IsActive() {
		j.RealtimeJourney = realtimeJourney
	}
}
func (j Journey) MarshalBinary() ([]byte, error) {
	return json.Marshal(j)
}
func (journey *Journey) GenerateFunctionalHash() string {
	hash := sha256.New()

	hash.Write([]byte(journey.ServiceRef))
	hash.Write([]byte(journey.DestinationDisplay))
	hash.Write([]byte(journey.Direction))
	hash.Write([]byte(journey.DepartureTime.String()))

	rules := append(journey.Availability.Condition, journey.Availability.Match...)
	rules = append(rules, journey.Availability.MatchSecondary...)
	rules = append(rules, journey.Availability.Exclude...)

	for _, availabilityMatchRule := range rules {
		hash.Write([]byte(availabilityMatchRule.Type))
		hash.Write([]byte(availabilityMatchRule.Value))
		hash.Write([]byte(availabilityMatchRule.Description))
	}

	for _, pathItem := range journey.Path {
		hash.Write([]byte(pathItem.OriginStopRef))
		hash.Write([]byte(pathItem.OriginArrivalTime.GoString()))
		hash.Write([]byte(pathItem.OriginDepartureTime.GoString()))
		hash.Write([]byte(pathItem.DestinationStopRef))
		hash.Write([]byte(pathItem.DestinationArrivalTime.GoString()))
	}

	return fmt.Sprintf("%x", hash.Sum(nil))
}
func (j Journey) FlattenStops() ([]string, map[string]time.Time, map[string]time.Time) {
	stops := []string{}
	arrivalTimes := map[string]time.Time{}
	departureTimes := map[string]time.Time{}
	alreadySeen := map[string]bool{}

	for _, pathItem := range j.Path {
		if !alreadySeen[pathItem.OriginStopRef] {
			stops = append(stops, pathItem.OriginStopRef)

			arrivalTimes[pathItem.OriginStopRef] = pathItem.OriginArrivalTime
			departureTimes[pathItem.OriginStopRef] = pathItem.OriginDepartureTime

			alreadySeen[pathItem.OriginStopRef] = true
		}
	}

	lastPathItem := j.Path[len(j.Path)-1]
	if !alreadySeen[lastPathItem.OriginStopRef] {
		stops = append(stops, lastPathItem.OriginStopRef)

		arrivalTimes[lastPathItem.OriginStopRef] = lastPathItem.OriginArrivalTime
		departureTimes[lastPathItem.OriginStopRef] = lastPathItem.OriginDepartureTime
	}

	return stops, arrivalTimes, departureTimes
}

func GetAvailableJourneys(journeysCollection *mongo.Collection, framedVehicleJourneyDate time.Time, query bson.M) []*Journey {
	journeys := []*Journey{}

	opts := options.Find().SetProjection(bson.D{
		bson.E{Key: "_id", Value: 0},
		bson.E{Key: "otheridentifiers", Value: 0},
		bson.E{Key: "datasource", Value: 0},
		bson.E{Key: "creationdatetime", Value: 0},
		bson.E{Key: "modificationdatetime", Value: 0},
		bson.E{Key: "destinationdisplay", Value: 0},
		bson.E{Key: "path.track", Value: 0},
		bson.E{Key: "path.originactivity", Value: 0},
		bson.E{Key: "path.destinationactivity", Value: 0},
	})
	cursor, _ := journeysCollection.Find(context.Background(), query, opts)

	for cursor.Next(context.TODO()) {
		var journey *Journey
		err := cursor.Decode(&journey)
		if err != nil {
			log.Error().Err(err).Msg("Failed to decode journey")
		}

		// if it has no availability then we'll just ignore it
		if journey.Availability != nil && journey.Availability.MatchDate(framedVehicleJourneyDate) {
			journeys = append(journeys, journey)
		}
	}

	return journeys
}

// The CTDF abstraction fails here are we only use siri-vm identifyinginformation
//
//	currently no other kind so is fine for now (TODO)
func IdentifyJourney(identifyingInformation map[string]string) (string, error) {
	currentTime := time.Now()

	// Get the directly referenced Operator
	var referencedOperator *Operator
	operatorRef := identifyingInformation["OperatorRef"]
	operatorsCollection := database.GetCollection("operators")
	query := bson.M{"$or": bson.A{bson.M{"primaryidentifier": operatorRef}, bson.M{"otheridentifiers": operatorRef}}}
	operatorsCollection.FindOne(context.Background(), query).Decode(&referencedOperator)

	if referencedOperator == nil {
		return "", errors.New("Could not find referenced Operator")
	}
	// referencedOperator.GetOperatorGroup()

	// TODO this is temporarily disabled as we're misidentifying journeys a lot
	// Get all potential Operators that belong in the Operator group
	// This is because *some* operator groups have incorrect operator IDs for a service
	var operators []string
	// if referencedOperator.OperatorGroup == nil {
	operators = append(operators, referencedOperator.OtherIdentifiers...)
	// } else {
	// 	referencedOperator.OperatorGroup.GetOperators()
	// 	for _, operator := range referencedOperator.OperatorGroup.Operators {
	// 		operators = append(operators, operator.OtherIdentifiers...)
	// 	}
	// }

	// Get the relevant Services
	var services []string
	serviceName := identifyingInformation["PublishedLineName"]
	if serviceName == "" {
		serviceName = identifyingInformation["ServiceNameRef"]
	}

	servicesCollection := database.GetCollection("services")

	cursor, _ := servicesCollection.Find(context.Background(), bson.M{
		"$and": bson.A{bson.M{"servicename": serviceName},
			bson.M{"operatorref": bson.M{"$in": operators}},
		},
	})

	for cursor.Next(context.TODO()) {
		var service *Service
		err := cursor.Decode(&service)
		if err != nil {
			log.Error().Err(err).Str("serviceName", serviceName).Msg("Failed to decode service")
		}

		services = append(services, service.PrimaryIdentifier)
	}

	serviceNameRegex, _ := regexp.Compile("^\\D+(\\d+)$")
	if len(services) == 0 {
		serviceNameMatch := serviceNameRegex.FindStringSubmatch(serviceName)

		if len(serviceNameMatch) == 2 {
			cursor, _ := servicesCollection.Find(context.Background(), bson.M{
				"$and": bson.A{bson.M{"servicename": serviceNameMatch[1]},
					bson.M{"operatorref": bson.M{"$in": operators}},
				},
			})

			for cursor.Next(context.TODO()) {
				var service *Service
				err := cursor.Decode(&service)
				if err != nil {
					log.Error().Err(err).Str("serviceName", serviceName).Msg("Failed to decode service")
				}

				services = append(services, service.PrimaryIdentifier)
			}
		}
	}

	if len(services) == 0 {
		return "", errors.New("Could not find related Service")
	}

	// Get the relevant Journeys
	var framedVehicleJourneyDate time.Time
	if identifyingInformation["FramedVehicleJourneyDate"] == "" {
		framedVehicleJourneyDate = time.Now()
	} else {
		framedVehicleJourneyDate, _ = time.Parse(YearMonthDayFormat, identifyingInformation["FramedVehicleJourneyDate"])

		// Fallback for dodgy formatted frames
		if framedVehicleJourneyDate.Year() < 2022 {
			framedVehicleJourneyDate = time.Now()
		}
	}

	journeys := []*Journey{}

	vehicleJourneyRef := identifyingInformation["VehicleJourneyRef"]
	blockRef := identifyingInformation["BlockRef"]
	journeysCollection := database.GetCollection("journeys")

	// First try getting Journeys by the TicketMachineJourneyCode
	if vehicleJourneyRef != "" {
		journeys = GetAvailableJourneys(journeysCollection, framedVehicleJourneyDate, bson.M{
			"$and": bson.A{
				bson.M{"serviceref": bson.M{"$in": services}},
				bson.M{"otheridentifiers.TicketMachineJourneyCode": vehicleJourneyRef},
			},
		})
		identifiedJourney, err := narrowJourneys(identifyingInformation, currentTime, journeys)
		if err == nil {
			return identifiedJourney.PrimaryIdentifier, nil
		}
	}

	// Fallback to Block Ref (incorrect usage of block ref but it kinda works)
	if blockRef != "" {
		journeys = GetAvailableJourneys(journeysCollection, framedVehicleJourneyDate, bson.M{
			"$and": bson.A{
				bson.M{"serviceref": bson.M{"$in": services}},
				bson.M{"otheridentifiers.BlockNumber": blockRef},
			},
		})
		identifiedJourney, err := narrowJourneys(identifyingInformation, currentTime, journeys)
		if err == nil {
			return identifiedJourney.PrimaryIdentifier, nil
		}
	}

	// If we fail with the ID codes then try with the origin & destination stops
	journeyQuery := []bson.M{}
	for _, service := range services {
		journeyQuery = append(journeyQuery, bson.M{"$or": bson.A{
			bson.M{
				"$and": bson.A{
					bson.M{"serviceref": service},
					bson.M{"path.originstopref": identifyingInformation["OriginRef"]},
				},
			},
			bson.M{
				"$and": bson.A{
					bson.M{"serviceref": service},
					bson.M{"path.destinationstopref": identifyingInformation["DestinationRef"]},
				},
			},
		}})
	}

	journeys = GetAvailableJourneys(journeysCollection, framedVehicleJourneyDate, bson.M{"$or": journeyQuery})

	identifiedJourney, err := narrowJourneys(identifyingInformation, currentTime, journeys)

	// if err != nil {
	// 	for _, v := range journeys {
	// 		if (v.Path[0].OriginStopRef == identifyingInformation["OriginRef"]) || (v.Path[len(v.Path)-1].DestinationStopRef == identifyingInformation["DestinationRef"]) {
	// 			pretty.Println(v.DepartureTime, identifyingInformation["OriginAimedDepartureTime"])
	// 		}
	// 	}
	// }

	if err == nil {
		return identifiedJourney.PrimaryIdentifier, nil
	} else {
		return "", err
	}
}

func narrowJourneys(identifyingInformation map[string]string, currentTime time.Time, journeys []*Journey) (*Journey, error) {
	journeys = FilterIdenticalJourneys(journeys)

	if len(journeys) == 0 {
		return nil, errors.New("Could not find related Journeys")
	} else if len(journeys) == 1 {
		return journeys[0], nil
	} else {
		timeFilteredJourneys := []*Journey{}

		// Filter based on exact time
		for _, journey := range journeys {
			originAimedDepartureTimeNoOffset, _ := time.Parse(XSDDateTimeFormat, identifyingInformation["OriginAimedDepartureTime"])
			originAimedDepartureTime := originAimedDepartureTimeNoOffset.In(currentTime.Location())

			if journey.DepartureTime.Hour() == originAimedDepartureTime.Hour() && journey.DepartureTime.Minute() == originAimedDepartureTime.Minute() {
				timeFilteredJourneys = append(timeFilteredJourneys, journey)
			}
		}

		// If fail exact time then give a few minute on each side a try if at least one of the start/end stops match
		allowedMinuteOffset := 5
		if len(timeFilteredJourneys) == 0 {
			for _, journey := range journeys {
				// Skip check if none of the start/end stops match
				if len(journey.Path) == 0 {
					continue
				}

				if !(journey.Path[0].OriginStopRef == identifyingInformation["OriginRef"] || journey.Path[len(journey.Path)-1].DestinationStopRef == identifyingInformation["DestinationRef"]) {
					continue
				}

				originAimedDepartureTimeNoOffset, _ := time.Parse(XSDDateTimeFormat, identifyingInformation["OriginAimedDepartureTime"])
				originAimedDepartureTime := originAimedDepartureTimeNoOffset.In(currentTime.Location())

				originAimedDepartureTimeDayMinutes := (originAimedDepartureTime.Hour() * 60) + originAimedDepartureTime.Minute()
				journeyDepartureTimeDayMinutes := (journey.DepartureTime.Hour() * 60) + journey.DepartureTime.Minute()
				dayMinuteDiff := originAimedDepartureTimeDayMinutes - journeyDepartureTimeDayMinutes

				if dayMinuteDiff <= allowedMinuteOffset && dayMinuteDiff >= (allowedMinuteOffset*-1) {
					timeFilteredJourneys = append(timeFilteredJourneys, journey)
				}
			}
		}

		if len(timeFilteredJourneys) == 0 {
			return nil, errors.New("Could not narrow down to single Journey with departure time. Now zero")
		} else if len(timeFilteredJourneys) == 1 {
			return timeFilteredJourneys[0], nil
		} else {
			return nil, errors.New("Could not narrow down to single Journey by time. Still many remaining")
		}
	}
}

func FilterIdenticalJourneys(journeys []*Journey) []*Journey {
	filtered := []*Journey{}

	matches := map[string]bool{}
	for _, journey := range journeys {
		hash := journey.GenerateFunctionalHash()

		if !matches[hash] {
			filtered = append(filtered, journey)
			matches[hash] = true
		}
	}

	return filtered
}

type JourneyPathItem struct {
	OriginStopRef      string `groups:"basic"`
	DestinationStopRef string `groups:"basic"`

	OriginStop      *Stop `groups:"basic"`
	DestinationStop *Stop `groups:"basic"`

	Distance int `groups:"basic"`

	OriginArrivalTime      time.Time `groups:"basic"`
	DestinationArrivalTime time.Time `groups:"basic"`

	OriginDepartureTime time.Time `groups:"basic"`

	DestinationDisplay string `groups:"basic"`

	OriginActivity      []JourneyPathItemActivity `groups:"basic"`
	DestinationActivity []JourneyPathItemActivity `groups:"basic"`

	Track []Location `groups:"basic"`
}

func (jpi *JourneyPathItem) GetReferences() {
	jpi.GetOriginStop()
	jpi.GetDestinationStop()
}
func (jpi *JourneyPathItem) GetOriginStop() {
	stopsCollection := database.GetCollection("stops")
	stopsCollection.FindOne(context.Background(), bson.M{"primaryidentifier": jpi.OriginStopRef}).Decode(&jpi.OriginStop)
}
func (jpi *JourneyPathItem) GetDestinationStop() {
	stopsCollection := database.GetCollection("stops")
	stopsCollection.FindOne(context.Background(), bson.M{"primaryidentifier": jpi.DestinationStopRef}).Decode(&jpi.DestinationStop)
}

type JourneyPathItemActivity string

const (
	JourneyPathItemActivityPickup  = "Pickup"
	JourneyPathItemActivitySetdown = "Setdown"
	JourneyPathItemActivityPass    = "Pass"
)
