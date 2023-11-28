package main

import (
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
	"regexp"
	"strconv"
	"strings"

	"github.com/br3w0r/goitopdf/itopdf"
	"github.com/schollz/progressbar/v3"
)

func main() {

	extractTitle := flag.Bool("extrectTitle", true, "used to decide if legacy naming system is used or title is extracted from anyflip")
	ocrFlag := flag.Bool("ocr", true, "used to toggle ocr feature")
	customName := flag.String("customName", "", "used to set output file name, overides extractTitle")
	anyflipURL, err := url.Parse(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}

	bookUrlPathElements := strings.Split(anyflipURL.Path, "/")
	// secect only 1st and 2nd element of url to avoid mobile on online.anyflip urls
	// as path starts with / offset index by 1
	anyflipURL.Path = path.Join("/", bookUrlPathElements[1], bookUrlPathElements[2])

	downloadFolder := path.Base(anyflipURL.String())
	outputFile := downloadFolder + ".pdf"

	configjs, err := downloadConfigJSFile(anyflipURL)
	if err != nil {
		log.Fatal(err)
	}

	//use custom name for output
	if *customName != "" {
		outputFile = *customName
	}

	// use --extract_title to automatically rename pdf to it's title from anyflip, default true
	if *extractTitle && *customName == "" {
		of, err := getBookTitle(anyflipURL, configjs)
		if (err != nil || of != "") && !strings.Contains(of, "://") {
			outputFile = of + ".pdf"
		}
	}

	fmt.Println("Preparing to download")
	pageCount, err := getPageCount(anyflipURL, configjs)
	if err != nil {
		log.Fatal(err)
	}
	err = downloadImages(anyflipURL, pageCount, downloadFolder)

	if err != nil {
		// try getting pages by id instead of page number, only encounterd it twice
		err = downloadImagesFallback(anyflipURL, pageCount, downloadFolder, configjs)
	}
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Converting to pdf")
	if *ocrFlag {
		err = createOCRPDF(outputFile, downloadFolder)
	} else {
		err = createPDF(outputFile, downloadFolder)
	}
	if err != nil {
		log.Fatal(err)
	}

	os.RemoveAll(downloadFolder)
	os.RemoveAll(downloadFolder + "_pdf")
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

func createPDF(outputFile string, imageDir string) error {
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

func getBookTitle(url *url.URL, configjs string) (string, error) {
	r := regexp.MustCompile("\"?(bookConfig\\.)?bookTitle\"?=\"(.*?)\"")
	offsett := 22
	match := r.FindString(configjs)
	if match == "" {
		offsett = 17
		r = regexp.MustCompile(`"meta":\{"title":"(.*?)"`)
	}
	// fmt.Println(configjs)
	match = r.FindString(configjs)
	if match == "" {
		return url.String(), errors.New("no title found")
	}

	match = match[offsett : len(match)-1]
	return match, nil
}

func getPageCount(url *url.URL, configjs string) (int, error) {

	r := regexp.MustCompile("\"?(bookConfig\\.)?totalPageCount\"?[=:]\"?\\d+\"?")
	match := r.FindString(configjs)
	if strings.Contains(match, "=") {
		match = strings.Split(match, "=")[1]
	} else if strings.Contains(match, ":") {
		match = strings.Split(match, ":")[1]
	} else {
		return 0, errors.New("could not find page count")
	}
	match = strings.ReplaceAll(match, "\"", "")
	return strconv.Atoi(match)
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

func parseConfigJSFile(configjs string) (map[string]interface{}, error) {
	var v map[string]interface{}
	// remove js variable notation
	configjs = configjs[17 : len(configjs)-1]
	err := json.Unmarshal([]byte(configjs), &v)
	return v, err
}
