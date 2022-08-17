package routes

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/britbus/britbus/pkg/ctdf"
	"github.com/britbus/britbus/pkg/database"
	"github.com/britbus/britbus/pkg/transforms"
	"github.com/gofiber/fiber/v2"
	"github.com/liip/sheriff"
	"github.com/rs/zerolog/log"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"

	iso8601 "github.com/senseyeio/duration"
)

func StopsRouter(router fiber.Router) {
	router.Get("/", listStops)
	router.Get("/:identifier", getStop)
	router.Get("/:identifier/departures", getStopDepartures)
}

func listStops(c *fiber.Ctx) error {
	boundsQuery, err := getBoundsQuery(c)
	if err != nil {
		c.SendStatus(fiber.StatusBadRequest)
		return c.JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	stops := []ctdf.Stop{}

	stopsCollection := database.GetCollection("stops")

	query := bson.M{"$and": bson.A{bson.M{"status": "active"}, bson.M{"location": boundsQuery}}}
	cursor, _ := stopsCollection.Find(context.Background(), query)

	for cursor.Next(context.TODO()) {
		var stop *ctdf.Stop
		err := cursor.Decode(&stop)
		if err != nil {
			log.Error().Err(err).Msg("Failed to decode Stop")
		}

		stops = append(stops, *stop)
	}

	c.JSON(stops)
	return nil
}

func getStop(c *fiber.Ctx) error {
	identifier := c.Params("identifier")

	stopsCollection := database.GetCollection("stops")
	var stop *ctdf.Stop
	stopsCollection.FindOne(context.Background(), bson.M{"primaryidentifier": identifier}).Decode(&stop)

	if stop == nil {
		c.SendStatus(fiber.StatusNotFound)
		return c.JSON(fiber.Map{
			"error": "Could not find Stop matching Stop Identifier",
		})
	} else {
		stop.GetServices()

		transforms.Transform(stop)

		return c.JSON(stop)
	}
}

func getStopDepartures(c *fiber.Ctx) error {
	stopIdentifier := c.Params("identifier")
	count, err := strconv.Atoi(c.Query("count", "25"))
	startDateTimeString := c.Query("datetime")

	if err != nil {
		c.SendStatus(fiber.StatusBadRequest)
		return c.JSON(fiber.Map{
			"error": "Parameter count should be an integer",
		})
	}

	stopsCollection := database.GetCollection("stops")
	var stop *ctdf.Stop
	stopsCollection.FindOne(context.Background(), bson.M{"primaryidentifier": stopIdentifier}).Decode(&stop)

	if stop == nil {
		c.SendStatus(fiber.StatusNotFound)
		return c.JSON(fiber.Map{
			"error": "Could not find Stop matching Stop Identifier",
		})
	}

	var startDateTime time.Time
	if startDateTimeString == "" {
		startDateTime = time.Now()
	} else {
		startDateTime, err = time.Parse(time.RFC3339, startDateTimeString)

		if err != nil {
			c.SendStatus(fiber.StatusBadRequest)
			return c.JSON(fiber.Map{
				"error":    "Parameter datetime should be an RFS3339/ISO8601 datetime",
				"detailed": err,
			})
		}
	}

	// Calculate tomorrows start date time by shifting current date time by 1 day and then setting hours/minutes/seconds to 0
	nextDayDuration, _ := iso8601.ParseISO8601("P1D")
	dayAfterDateTime := nextDayDuration.Shift(startDateTime)
	dayAfterDateTime = time.Date(
		dayAfterDateTime.Year(), dayAfterDateTime.Month(), dayAfterDateTime.Day(), 0, 0, 0, 0, dayAfterDateTime.Location(),
	)

	journeys := []*ctdf.Journey{}

	journeysCollection := database.GetCollection("journeys")
	currentTime := time.Now()

	// This projection excludes values we dont care about - the main ones being path.*
	// Reduces memory usage and execution time
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
	cursor, _ := journeysCollection.Find(context.Background(), bson.M{"path.originstopref": stopIdentifier}, opts)

	if err = cursor.All(context.Background(), &journeys); err != nil {
		log.Error().Err(err).Msg("Failed to decode Stop")
	}

	log.Debug().Str("Length", (time.Now().Sub(currentTime).String())).Msg("Database lookup")

	realtimeTimeframe := startDateTime.Format("2006-01-02")

	var journeysTimetable []*ctdf.TimetableRecord

	currentTime = time.Now()
	journeysTimetableToday := ctdf.GenerateTimetableFromJourneys(journeys, stopIdentifier, startDateTime, realtimeTimeframe, true)
	log.Debug().Str("Length", (time.Now().Sub(currentTime).String())).Msg("Timetable generation today")

	// If not enough journeys in todays timetable then look into tomorrows
	if len(journeysTimetableToday) < count {
		currentTime = time.Now()
		journeysTimetableTomorrow := ctdf.GenerateTimetableFromJourneys(journeys, stopIdentifier, dayAfterDateTime, realtimeTimeframe, false)
		log.Debug().Str("Length", (time.Now().Sub(currentTime).String())).Msg("Timetable generation tomorrow")

		journeysTimetable = append(journeysTimetableToday, journeysTimetableTomorrow...)
	} else {
		journeysTimetable = journeysTimetableToday
	}

	// Sort timetable by TimetableRecord time
	sort.Slice(journeysTimetable, func(i, j int) bool {
		return journeysTimetable[i].Time.Before(journeysTimetable[j].Time)
	})

	// Once sorted cut off any records higher than our max count
	if len(journeysTimetable) > count {
		journeysTimetable = journeysTimetable[:count]
	}

	currentTime = time.Now()
	transforms.Transform(journeysTimetable)
	log.Debug().Str("Length", (time.Now().Sub(currentTime).String())).Msg("Transform")

	journeysTimetableReduced, err := sheriff.Marshal(&sheriff.Options{
		Groups: []string{"basic"},
	}, journeysTimetable)

	if err != nil {
		c.SendStatus(fiber.StatusInternalServerError)
		return c.JSON(fiber.Map{
			"error": "Sherrif could not reduce journeysTimetable",
		})
	}

	return c.JSON(journeysTimetableReduced)
}
