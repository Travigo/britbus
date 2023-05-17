package stats

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"cloud.google.com/go/storage"
	"github.com/rs/zerolog/log"
	"github.com/travigo/travigo/pkg/ctdf"
	"github.com/travigo/travigo/pkg/elastic_client"
	"github.com/ulikunitz/xz"
	"google.golang.org/api/iterator"
)

type Indexer struct {
	CloudBucketName string

	operators      map[string]*ctdf.Operator
	operatorGroups map[string]*ctdf.OperatorGroup
	stops          map[string]*ctdf.Stop
	services       map[string]*ctdf.Service

	journeyHistoryIndexName      string
	journeyStopActivityIndexName string
}

func (i *Indexer) Perform() {
	currentTime := time.Now()
	yearNumber, weekNumber := currentTime.ISOWeek()
	i.journeyHistoryIndexName = fmt.Sprintf("journey-history-%d-%d", yearNumber, weekNumber)
	i.journeyStopActivityIndexName = fmt.Sprintf("journey-stop-activity-%d-%d", yearNumber, weekNumber)

	// file, _ := os.Open("/Users/aaronclaydon/Downloads/test_data.tar.xz")
	// i.indexJourneysBundle("2022-06-06T04 00 02+01 00.tar.xz", file)
	// file.Close()

	// return

	client, err := storage.NewClient(context.Background())
	if err != nil {
		log.Fatal().Err(err).Msg("Could not create GCP storage client")
	}

	bucket := client.Bucket(i.CloudBucketName)

	objects := bucket.Objects(context.TODO(), nil)

	for {
		objectAttr, err := objects.Next()

		if err == iterator.Done {
			break
		}
		if err != nil {
			log.Error().Err(err).Msg("Failed to iterate over bucket")
		}

		log.Info().Msgf("Found bundle file %s", objectAttr.Name)

		bundleAlreadyIndexed := i.bundleIndexed(objectAttr.Name)

		if bundleAlreadyIndexed {
			log.Info().Msgf("Bundle file %s already indexed", objectAttr.Name)
		} else {
			object := bucket.Object(objectAttr.Name)
			storageReader, err := object.NewReader(context.Background())
			if err != nil {
				log.Error().Err(err).Msgf("Failed to get object %s", objectAttr.Name)
			}

			i.indexJourneysBundle(objectAttr.Name, storageReader)
		}
	}
}

func (i *Indexer) bundleIndexed(bundleName string) bool {
	var queryBytes bytes.Buffer
	query := map[string]interface{}{
		"query": map[string]interface{}{
			"term": map[string]interface{}{
				"BundleSourceFile.keyword": bundleName,
			},
		},
	}
	json.NewEncoder(&queryBytes).Encode(query)
	res, err := elastic_client.Client.Count(
		elastic_client.Client.Count.WithContext(context.Background()),
		elastic_client.Client.Count.WithIndex("journey-history-*"),
		elastic_client.Client.Count.WithBody(&queryBytes),
		elastic_client.Client.Count.WithPretty(),
	)

	if err != nil {
		log.Fatal().Err(err).Msg("Failed to query index")
	}

	responseBytes, _ := io.ReadAll(res.Body)
	var responseMap map[string]interface{}
	json.Unmarshal(responseBytes, &responseMap)

	if responseMap["status"] != nil {
		log.Fatal().Str("response", string(responseBytes)).Msg("Failed to query index")
	}

	if int(responseMap["count"].(float64)) > 0 {
		return true
	}

	return false
}

func (i *Indexer) indexJourneysBundle(bundleName string, file io.Reader) {
	i.operators = map[string]*ctdf.Operator{}
	i.operatorGroups = map[string]*ctdf.OperatorGroup{}
	i.stops = map[string]*ctdf.Stop{}
	i.services = map[string]*ctdf.Service{}

	log.Info().Msgf("Indexing bundle file %s", bundleName)

	xzReader, err := xz.NewReader(file)
	if err != nil {
		log.Error().Err(err).Msg("Failed to decompress xz file")
	}

	tarReader := tar.NewReader(xzReader)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}

		fileBytes, _ := io.ReadAll(tarReader)

		if header.Name == "stops.json" {
			i.parseStopsFile(bundleName, fileBytes)
		} else if header.Name == "operators.json" {
			i.parseOperatorsFile(bundleName, fileBytes)
		} else if header.Name == "operator_groups.json" {
			i.parseOperatorGroupsFile(bundleName, fileBytes)
		} else if header.Name == "services.json" {
			i.parseServicesFile(bundleName, fileBytes)
		} else {
			i.parseArchivedJourneyFile(bundleName, fileBytes)
		}
	}

	log.Info().Msgf("Bundle file %s indexing complete", bundleName)
}

func (i *Indexer) parseStopsFile(bundleName string, contents []byte) {
	var stops []*ctdf.Stop
	err := json.Unmarshal(contents, &stops)
	if err != nil {
		log.Error().Err(err).Msg("Failed to decode stops JSON file")
	}

	for _, stop := range stops {
		i.stops[stop.PrimaryIdentifier] = stop
	}
}

func (i *Indexer) parseOperatorsFile(bundleName string, contents []byte) {
	var operators []*ctdf.Operator
	err := json.Unmarshal(contents, &operators)
	if err != nil {
		log.Error().Err(err).Msg("Failed to decode operators JSON file")
	}

	for _, operator := range operators {
		i.operators[operator.PrimaryIdentifier] = operator

		for _, secondaryIdentifier := range operator.OtherIdentifiers {
			i.operators[secondaryIdentifier] = operator
		}
	}
}

func (i *Indexer) parseOperatorGroupsFile(bundleName string, contents []byte) {
	var operatorGroups []*ctdf.OperatorGroup
	err := json.Unmarshal(contents, &operatorGroups)
	if err != nil {
		log.Error().Err(err).Msg("Failed to decode operatorGroups JSON file")
	}

	for _, operatorGroup := range operatorGroups {
		i.operatorGroups[operatorGroup.Identifier] = operatorGroup
	}
}

func (i *Indexer) parseServicesFile(bundleName string, contents []byte) {
	var services []*ctdf.Service
	err := json.Unmarshal(contents, &services)
	if err != nil {
		log.Error().Err(err).Msg("Failed to decode service JSON file")
	}

	for _, service := range services {
		i.services[service.PrimaryIdentifier] = service
	}
}

func (i *Indexer) parseArchivedJourneyFile(bundleName string, contents []byte) {
	var ctdfArchivedJourney *ctdf.ArchivedJourney
	err := json.Unmarshal(contents, &ctdfArchivedJourney)
	if err != nil {
		log.Error().Err(err).Msg("Failed to decode JSON file")
	}

	operator := i.operators[ctdfArchivedJourney.OperatorRef]

	// Convert to the extended stats Archived Journey form
	archivedJourney := ArchivedJourney{
		ArchivedJourney:  *ctdfArchivedJourney,
		BundleSourceFile: bundleName,

		PrimaryOperatorRef: operator.PrimaryIdentifier,
		OperatorGroupRef:   operator.OperatorGroupRef,

		Regions: operator.Regions,
	}

	archivedJourneyBytes, _ := json.Marshal(archivedJourney)

	elastic_client.IndexRequest(i.journeyHistoryIndexName, bytes.NewReader(archivedJourneyBytes))

	// Go ahead and create a journey stop activity index event
	// Contains stop arrival time, co-ordinates, service
	stopTimestampDate := archivedJourney.CreationDateTime
	dateRolledOver := false

	for index, archivedJourneyStop := range archivedJourney.Stops {
		stop := i.stops[archivedJourneyStop.StopRef]

		if stop == nil {
			continue
		}

		var stopTimestamp time.Time

		if index > 0 {
			lastArchivedJourneyStop := archivedJourney.Stops[index-1]

			if !dateRolledOver && lastArchivedJourneyStop.ExpectedArrivalTime.Sub(archivedJourneyStop.ExpectedArrivalTime).Seconds() > 0 {
				dateRolledOver = true
				stopTimestampDate = stopTimestampDate.Add(24 * time.Hour)
			}
		}

		if archivedJourneyStop.HasActualData {
			stopTimestamp = time.Date(
				stopTimestampDate.Year(),
				stopTimestampDate.Month(),
				stopTimestampDate.Day(),
				archivedJourneyStop.ActualArrivalTime.Hour(),
				archivedJourneyStop.ActualArrivalTime.Minute(),
				archivedJourneyStop.ActualArrivalTime.Second(),
				archivedJourneyStop.ActualArrivalTime.Nanosecond(),
				archivedJourneyStop.ActualArrivalTime.Location(),
			)
		} else {
			stopTimestamp = time.Date(
				stopTimestampDate.Year(),
				stopTimestampDate.Month(),
				stopTimestampDate.Day(),
				archivedJourneyStop.ExpectedArrivalTime.Hour(),
				archivedJourneyStop.ExpectedArrivalTime.Minute(),
				archivedJourneyStop.ExpectedArrivalTime.Second(),
				archivedJourneyStop.ExpectedArrivalTime.Nanosecond(),
				archivedJourneyStop.ExpectedArrivalTime.Location(),
			)
		}

		journeyStopActivity := ctdf.ArchivedJourneyStopActivity{
			Timestamp: stopTimestamp,

			Location: ctdf.ElasticGeoPoint{
				Lat: stop.Location.Coordinates[1],
				Lon: stop.Location.Coordinates[0],
			},
		}

		journeyStopActivityBytes, _ := json.Marshal(journeyStopActivity)

		elastic_client.IndexRequest(i.journeyStopActivityIndexName, bytes.NewReader(journeyStopActivityBytes))
	}
}
