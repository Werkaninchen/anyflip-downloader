package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"

	"github.com/asaskevich/govalidator"
	"github.com/br3w0r/goitopdf/itopdf"
	"github.com/schollz/progressbar/v3"
)

var title string
var tempDownloadFolder string
var insecure bool
var ocr bool

type flipbook struct {
	URL       *url.URL
	title     string
	pageCount int
	pageURLs  []string
}

func init() {
	flag.StringVar(&tempDownloadFolder, "temp-download-folder", "", "Specifies the name of the temporary download folder")
	flag.StringVar(&title, "title", "", "Specifies the name of the generated PDF document (uses book title if not specified)")
	flag.BoolVar(&insecure, "insecure", false, "Skip certificate validation")
	flag.BoolVar(&ocr, "ocr", true, "used to toggle ocr feature")
}

func main() {
	flag.Parse()
	anyflipURL, err := url.Parse(flag.Args()[0])
	if err != nil {
		log.Fatal(err)
	}

	if insecure {
		fmt.Println("You enabled insecure downloads. This disables security checks. Stay safe!")
		http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	fmt.Println("Preparing to download")
	flipbook, err := prepareDownload(anyflipURL)
	if err != nil {
		log.Fatal(err)
	}

	if tempDownloadFolder == "" {
		tempDownloadFolder = flipbook.title
	}
	outputFile := title + ".pdf"

	err = flipbook.downloadImages(tempDownloadFolder)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Converting to pdf")
	if ocr {
		err = createOCRPDF(outputFile, tempDownloadFolder)
	} else {
		err = createPDF(outputFile, tempDownloadFolder)
	}

	if err != nil {
		log.Fatal(err)
	}

	os.RemoveAll(tempDownloadFolder)
}

func prepareDownload(anyflipURL *url.URL) (*flipbook, error) {
	var newFlipbook flipbook

	sanitizeURL(anyflipURL)
	newFlipbook.URL = anyflipURL

	configjs, err := downloadConfigJSFile(anyflipURL)
	if err != nil {
		return nil, err
	}

	if title == "" {
		title, err = getBookTitle(configjs)
		if err != nil {
			title = path.Base(anyflipURL.String())
		}
	}

	newFlipbook.title = govalidator.SafeFileName(title)
	newFlipbook.pageCount, err = getPageCount(configjs)
	pageFileNames := getPageFileNames(configjs)

	downloadURL, _ := url.Parse("https://online.anyflip.com/")
	println(newFlipbook.URL.String())
	if len(pageFileNames) == 0 {
		for i := 1; i <= newFlipbook.pageCount; i++ {
			downloadURL.Path = path.Join(newFlipbook.URL.Path, "files", "mobile", strconv.Itoa(i)+".jpg")
			newFlipbook.pageURLs = append(newFlipbook.pageURLs, downloadURL.String())
		}
	} else {
		for i := 0; i < newFlipbook.pageCount; i++ {
			downloadURL.Path = path.Join(newFlipbook.URL.Path, "files", "large", pageFileNames[i])
			newFlipbook.pageURLs = append(newFlipbook.pageURLs, downloadURL.String())
		}
	}

	return &newFlipbook, err
}

func sanitizeURL(anyflipURL *url.URL) {
	bookURLPathElements := strings.Split(anyflipURL.Path, "/")
	anyflipURL.Path = path.Join("/", bookURLPathElements[1], bookURLPathElements[2])
}

func createPDF(outputFile string, imageDir string) error {
	outputFile = strings.ReplaceAll(outputFile, "'", "")
	outputFile = strings.ReplaceAll(outputFile, "\\", "")
	outputFile = strings.ReplaceAll(outputFile, ":", "")

	pdf := itopdf.NewInstance()
	err := pdf.WalkDir(imageDir, nil)
	if err != nil {
		return err
	}
	err = pdf.Save(outputFile)
	if err != nil {
		return err
	}
	return nil
}

func (fb *flipbook) downloadImages(downloadFolder string) error {
	err := os.Mkdir(downloadFolder, os.ModePerm)
	if err != nil {
		return err
	}

	bar := progressbar.NewOptions(fb.pageCount,
		progressbar.OptionFullWidth(),
		progressbar.OptionSetPredictTime(false),
		progressbar.OptionShowCount(),
		progressbar.OptionSetDescription("Downloading"),
	)

	for page := 0; page < fb.pageCount; page++ {
		downloadURL := fb.pageURLs[page]
		response, err := http.Get(downloadURL)
		if err != nil {
			return err
		}

		if response.StatusCode != http.StatusOK {
			println("During download from ", downloadURL)
			return errors.New("Received non-200 response: " + response.Status)
		}

		extension := path.Ext(downloadURL)
		filename := fmt.Sprintf("%04d%v", page, extension)
		file, err := os.Create(path.Join(downloadFolder, filename))
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(file, response.Body)
		if err != nil {
			return err
		}

		bar.Add(1)
	}
	fmt.Println()
	return nil
}

func downloadConfigJSFile(bookURL *url.URL) (string, error) {
	configjsURL, err := url.Parse("https://online.anyflip.com")
	if err != nil {
		return "", err
	}
	configjsURL.Path = path.Join(bookURL.Path, "mobile", "javascript", "config.js")
	resp, err := http.Get(configjsURL.String())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("received non-200 response:" + resp.Status)
	}
	configjs, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(configjs), nil
}

// depends on tesseract and qpdf
func createOCRPDF(outputFile string, imgDir string) error {
	entries, err := os.ReadDir(imgDir)
	if err != nil {
		return err
	}
	bar := progressbar.NewOptions(len(entries),
		progressbar.OptionFullWidth(),
		progressbar.OptionSetPredictTime(false),
		progressbar.OptionShowCount(),
		progressbar.OptionSetDescription("OCR"),
	)

	os.Mkdir(imgDir+"_pdf", os.ModePerm)
	//ocr
	bar.Add(0)
	for entrie := range entries {
		entrieName := entries[entrie].Name()

		cmd := exec.Command("tesseract", entrieName, "../"+imgDir+"_pdf/"+entrieName, "pdf")
		cmd.Dir = imgDir

		err = cmd.Run()
		if err != nil {
			return err
		}
		bar.Add(1)

	}

	pdf_entries, err := os.ReadDir(imgDir + "_pdf")
	if err != nil {
		return err
	}

	args := []string{"--empty", "--pages"}

	for pdf_entrie := range pdf_entries {
		args = append(args, pdf_entries[pdf_entrie].Name())
	}
	args = append(args, "--", "../"+outputFile)

	cmd := exec.Command("qpdf", args...)
	cmd.Dir = imgDir + "_pdf"
	err = cmd.Run()

	return err
}

func downloadImages(url *url.URL, pageCount int, downloadFolder string) error {
	err := os.Mkdir(downloadFolder, os.ModePerm)
	if err != nil {
		return err
	}

	bar := progressbar.NewOptions(pageCount,
		progressbar.OptionFullWidth(),
		progressbar.OptionSetPredictTime(false),
		progressbar.OptionShowCount(),
		progressbar.OptionSetDescription("Downloading"),
	)
	downloadURL, err := url.Parse("https://online.anyflip.com")
	if err != nil {
		return err
	}

	for page := 1; page <= pageCount; page++ {

		downloadURL.Path = path.Join(url.Path, "files", "mobile", strconv.Itoa(page)+".jpg")
		response, err := http.Get(downloadURL.String())
		if err != nil {
			return err
		}

		if response.StatusCode != http.StatusOK {
			return errors.New("Received non-200 response: " + response.Status)
		}

		extension := path.Ext(downloadURL.String())
		filename := fmt.Sprintf("%04d%v", page, extension)
		file, err := os.Create(path.Join(downloadFolder, filename))
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(file, response.Body)
		if err != nil {
			return err
		}

		bar.Add(1)
	}
	fmt.Println()
	return nil
}

// ugly workaround for newer? uploads using fliphtml5 format
func downloadImagesFallback(url *url.URL, pageCount int, downloadFolder string, configjs string) error {

	v, err := parseConfigJSFile(configjs)
	if err != nil {
		log.Fatal(err)
	}

	bar := progressbar.NewOptions(pageCount,
		progressbar.OptionFullWidth(),
		progressbar.OptionSetPredictTime(false),
		progressbar.OptionShowCount(),
		progressbar.OptionSetDescription("Downloading"),
	)
	downloadURL, err := url.Parse("https://online.anyflip.com")
	if err != nil {
		return err
	}

	s, ok := v["fliphtml5_pages"].([]interface{})

	if !ok {
		return errors.New("error parsing")
	}
	for page, p := range s {
		m, ok := p.(map[string]interface{})
		if !ok {
			return errors.New("error parsing")
		}
		val, ok := m["n"].([]interface{})
		if !ok {
			return errors.New("error parsing")
		}
		pageId, ok := val[0].(string)
		if !ok {
			return errors.New("error parsing")
		}

		downloadURL.Path = path.Join(url.Path, "files", "large", pageId)
		response, err := http.Get(downloadURL.String())
		if err != nil {
			return err
		}

		if response.StatusCode != http.StatusOK {
			return errors.New("Received non-200 response: " + response.Status)
		}

		extension := path.Ext(downloadURL.String())
		filename := fmt.Sprintf("%04d%v", page, extension)
		file, err := os.Create(path.Join(downloadFolder, filename))
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(file, response.Body)
		if err != nil {
			return err
		}

		bar.Add(1)
	}
	fmt.Println()
	return nil
}

func parseConfigJSFile(configjs string) (map[string]interface{}, error) {
	var v map[string]interface{}
	// remove js variable notation
	configjs = configjs[17 : len(configjs)-1]
	err := json.Unmarshal([]byte(configjs), &v)
	return v, err
}
