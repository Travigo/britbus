package ctdf

import (
	"context"
	"time"

	"github.com/britbus/britbus/pkg/database"
	"go.mongodb.org/mongo-driver/bson"
)

type Journey struct {
	PrimaryIdentifier string            `groups:"basic"`
	OtherIdentifiers  map[string]string `groups:"basic"`

	CreationDateTime     string `groups:"detailed"`
	ModificationDateTime string `groups:"detailed"`

	DataSource *DataSource `groups:"detailed"`

	ServiceRef string   `groups:"internal"`
	Service    *Service `groups:"basic" json:",omitempty" bson:"-"`

	OperatorRef string    `groups:"internal"`
	Operator    *Operator `groups:"basic" json:",omitempty" bson:"-"`

	Direction          string    `groups:"detailed"`
	DepartureTime      time.Time `groups:"basic"`
	DestinationDisplay string    `groups:"basic"`

	Availability *Availability `groups:"detailed"`

	Path []JourneyPathItem `groups:"detailed"`
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

type JourneyPathItem struct {
	OriginStopRef      string
	DestinationStopRef string

	Distance int

	OriginArivalTime      time.Time
	DestinationArivalTime time.Time

	OriginDepartureTime time.Time

	DestinationDisplay string

	OriginActivity      []JourneyPathItemActivity
	DestinationActivity []JourneyPathItemActivity
}

type JourneyPathItemActivity string

const (
	JourneyPathItemActivityPickup  = "Pickup"
	JourneyPathItemActivitySetdown = "Setdown"
	JourneyPathItemActivityPass    = "Pass"
)
