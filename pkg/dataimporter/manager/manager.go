package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/travigo/travigo/pkg/ctdf"
	"github.com/travigo/travigo/pkg/database"
	"github.com/travigo/travigo/pkg/dataimporter/formats"
	"github.com/travigo/travigo/pkg/dataimporter/formats/naptan"
	"github.com/travigo/travigo/pkg/dataimporter/formats/nationalrailtoc"
	"github.com/travigo/travigo/pkg/dataimporter/formats/travelinenoc"
	"go.mongodb.org/mongo-driver/bson"
)

func GetDataset(identifier string) (DataSet, error) {
	registered := GetRegisteredDataSets()

	for _, dataset := range registered {
		if dataset.Identifier == identifier {
			return dataset, nil
		}
	}

	return DataSet{}, errors.New("Dataset could not be found")
}

func ImportDataset(identifier string) error {
	dataset, err := GetDataset(identifier)

	if err != nil {
		return err
	}

	log.Info().
		Str("identifier", dataset.Identifier).
		Str("format", string(dataset.Format)).
		Str("provider", dataset.Provider.Name).
		Interface("supports", dataset.SupportedObjects).
		Msg("Found dataset")

	if dataset.UnpackBundle != BundleFormatNone {
		return errors.New("Cannot handle bundled type yet")
	}

	var format formats.Format

	switch dataset.Format {
	case DataSetFormatTravelineNOC:
		format = &travelinenoc.TravelineData{}
	case DataSetFormatNaPTAN:
		format = &naptan.NaPTAN{}
	case DataSetFormatNationalRailTOC:
		format = &nationalrailtoc.TrainOperatingCompanyList{}
	default:
		return errors.New(fmt.Sprintf("Unrecognised format %s", dataset.Format))
	}

	source := dataset.Source
	if isValidUrl(dataset.Source) {
		var tempFile *os.File
		tempFile, _ = tempDownloadFile(dataset)

		source = tempFile.Name()
		defer os.Remove(tempFile.Name())
	}

	file, err := os.Open(source)
	if err != nil {
		return err
	}

	err = format.ParseFile(file)
	if err != nil {
		return err
	}

	datasource := &ctdf.DataSource{
		OriginalFormat: string(dataset.Format),
		Provider:       dataset.Provider.Name,
		DatasetID:      dataset.Identifier,
		Timestamp:      fmt.Sprintf("%d", time.Now().Unix()),
	}

	err = format.ImportIntoMongoAsCTDF(
		dataset.Identifier,
		dataset.SupportedObjects,
		datasource,
	)
	if err != nil {
		return err
	}

	if dataset.SupportedObjects.Stops {
		cleanupOldRecords("stops", datasource)
	}
	if dataset.SupportedObjects.StopGroups {
		cleanupOldRecords("stop_groups", datasource)
	}
	if dataset.SupportedObjects.Operators {
		cleanupOldRecords("operators", datasource)
	}
	if dataset.SupportedObjects.OperatorGroups {
		cleanupOldRecords("operator_groups", datasource)
	}
	if dataset.SupportedObjects.Services {
		cleanupOldRecords("services", datasource)
	}
	if dataset.SupportedObjects.Journeys {
		cleanupOldRecords("journeys", datasource)
	}

	return nil
}

func isValidUrl(toTest string) bool {
	_, err := url.ParseRequestURI(toTest)
	if err != nil {
		return false
	}

	u, err := url.Parse(toTest)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}

	return true
}

func tempDownloadFile(dataset DataSet) (*os.File, string) {
	req, _ := http.NewRequest("GET", dataset.Source, nil)
	req.Header.Set("user-agent", "curl/7.54.1") // TfL is protected by cloudflare and it gets angry when no user agent is set

	if dataset.DownloadHandler != nil {
		dataset.DownloadHandler(req)
	}

	client := &http.Client{}
	resp, err := client.Do(req)

	if err != nil {
		log.Fatal().Err(err).Msg("Download file")
	}
	defer resp.Body.Close()

	_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	fileExtension := filepath.Ext(dataset.Source)
	if err == nil {
		fileExtension = filepath.Ext(params["filename"])
	}

	tmpFile, err := os.CreateTemp(os.TempDir(), "travigo-data-importer-")
	if err != nil {
		log.Fatal().Err(err).Msg("Cannot create temporary file")
	}

	io.Copy(tmpFile, resp.Body)

	return tmpFile, fileExtension
}

func cleanupOldRecords(collectionName string, datasource *ctdf.DataSource) {
	collection := database.GetCollection(collectionName)

	query := bson.M{
		"$and": bson.A{
			bson.M{"datasource.originalformat": datasource.OriginalFormat},
			bson.M{"datasource.provider": datasource.Provider},
			bson.M{"datasource.datasetid": datasource.DatasetID},
			bson.M{"datasource.timestamp": bson.M{
				"$ne": datasource.Timestamp,
			}},
		},
	}

	result, _ := collection.DeleteMany(context.Background(), query)

	if result != nil {
		log.Info().
			Str("collection", collectionName).
			Int64("num", result.DeletedCount).
			Msg("Cleaned up old records")
	}
}
