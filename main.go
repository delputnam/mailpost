// Copyright © 2015 Del Putnam <del@putnams.net>.
//
// Licensed under the Simple Public License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://opensource.org/licenses/Simple-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"flag"
    "image"
    "image/color"
    "image/draw"
	"image/jpeg"
 	_ "image/png"
	"io"
	"io/ioutil"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	
	"github.com/BurntSushi/toml"
	"github.com/mxk/go-imap/imap"
	"github.com/nfnt/resize"
	"gopkg.in/alexcesaro/quotedprintable.v2"
	"gopkg.in/yaml.v2"
)

var wd, _ = os.Getwd()
var conf = flag.String("conf", wd+"/mailpost.toml", "Path to config file.")
var logfile = flag.String("log", wd+"/mailpost.log", "Path to log file.")
var interval = flag.String("interval", "5m", "Time between each check. Examples: 10s, 5m, 1h")
var debug = flag.Bool("debug", false, "Log all IMAP commands and responses.")
var once = flag.Bool("once", true, "Only execute the fetch once and exit.")

type Config struct {
	Server      string
	User        string
	Password    string
	ImageDir	string
	PostDir		string
	DatePathFmt	string
	BaseURL		string
	ImagePath	string
	MaxImgWidth	uint
	PostFrom	string
}

type Image struct {
	OrigURL		string
	OrigName	string
	Name		string
	Path		string
	URL			string
	Data    	[]byte
}

type Post struct {
	Title		string
	Date		string
	Type		string
	File		string
	Path 		string
	URL			string
	Data		string
}

type PathParts struct {
	Date		string
	Type		string
}

type Mailpost struct {
	config	Config
	client	*imap.Client
	images	[]Image
	posts	[]Post
}

func (m *Mailpost) Connect() {
	var err error
	log.Print("Connecting to server..\n")
	m.client, err = imap.DialTLS(m.config.Server, &tls.Config{})

	if err != nil {
		log.Fatalf("Connection to server failed: %s", err)
	}

	if m.client.State() == imap.Login {
		log.Print("Logging in..\n")
		m.client.Login(m.config.User, m.config.Password)
	}

	log.Print("Opening INBOX..\n")
	m.client.Select("INBOX", false)
}

func (m *Mailpost) DecodeSubject(msg *mail.Message) string {
	s, _, err := quotedprintable.DecodeHeader(msg.Header.Get("Subject"))

	if err != nil {
		return msg.Header.Get("Subject")
	} else {
		return s
	}
}

func (m *Mailpost) MakeDatePathPart(dateInfo string) string {
	const dateStringLayout = "2006-01-02"
	t, _ := time.Parse(dateStringLayout, dateInfo)
	return t.Format(m.config.DatePathFmt)
}

func (m *Mailpost) MakePathFromTemplate(pathTemplate string, pathData PathParts) string {
	if pathData.Type != "" {
		pathTemplate = strings.Replace(pathTemplate, "<type>", strings.Trim(pathData.Type, " "), 1)
	}
	if pathData.Date != "" {
		pathTemplate = strings.Replace(pathTemplate, "<date>", pathData.Date, 1)
	}
	return pathTemplate
}

func (m *Mailpost) MakePostPath(postInfo Post) string {
	datePathPart := m.MakeDatePathPart(postInfo.Date)
		
	postInfo.Path = strings.Replace(postInfo.Path, "<type>", strings.Trim(postInfo.Type, " "), 1)
	postInfo.Path = strings.Replace(postInfo.Path, "<date>", datePathPart, 1)
		
	err := os.MkdirAll(postInfo.Path, 0755)
	if err != nil {
		log.Fatal("Couldn't make path %s: %s", postInfo.Path, err)
	}
	
	return postInfo.Path
}

func (m *Mailpost) MakeDatePath(basePath string) (fullPath string, datePathPart string) {
	t := time.Now()
	datePathPart = t.Format("2006/01")
	
	fullPath = strings.Replace(basePath, "<date>", datePathPart, 1)
			
	err := os.MkdirAll(fullPath, 0755)
	if err != nil {
		log.Fatalf("Couldn't make date path: %s", err)
	}

	return fullPath, datePathPart
}

func (m *Mailpost) SanitizeFilename(name string) string {
	re := regexp.MustCompile(`[^\w\.]`)
	return re.ReplaceAllString(strings.ToLower(name), "_")
}

func (m *Mailpost) ExtractAttachment(r io.Reader, params map[string]string) {
	multipartReader := multipart.NewReader(r, params["boundary"])
	for {
		
		// ----------------------------------------
		// Read the next mime part
		mimePart, err := multipartReader.NextPart()
		if err == io.EOF {
			break
		} else if err != nil {
			log.Fatalf("Error parsing part: %s", err)
		}
		contentType, params, _ := mime.ParseMediaType(mimePart.Header.Get("Content-Type"))
		

		// ------------------------------------------
		// Check for an another multipart section
		if m.HasMultipart(contentType) {
			m.ExtractAttachment(mimePart, params)
			
		// ------------------------------------------
		// Check for an image part
		} else if m.HasImage(contentType) {
					  
			var imageInfo Image

			imageInfo.OrigName = mimePart.FileName()
									
			r := base64.NewDecoder(base64.StdEncoding, mimePart)			
		    imageInfo.Data, err = ioutil.ReadAll(r)
		    
			//m.SaveImage(imageInfo)			
		    m.ExtractImageData(imageInfo)
		
		// --------------------------------------------	
		// Check for a text part	
		} else if m.HasText(contentType) {
			buf := new(bytes.Buffer)
			_, err := io.Copy(buf, mimePart)
			if err != nil {
				log.Fatalf("Error copying body of post to buffer: %s", err)
			}
			
			m.ExtractPostData(buf.String())
		}
	}
}

func (m *Mailpost) FetchMails() {
	log.Print("Fetching unread UIDs..\n")
	cmd, err := m.client.UIDSearch("1:* NOT SEEN")
	cmd.Result(imap.OK)

	if err != nil {
		log.Fatalf("UIDSearch failed: %s", err)
	}

	uids := cmd.Data[0].SearchResults()
	if len(uids) == 0 {
		log.Print("No unread messages found.")
		return
	}

	log.Print("Fetching mail bodies..\n")
	set, _ := imap.NewSeqSet("")
	set.AddNum(uids...)
	cmd, err = m.client.UIDFetch(set, "UID", "FLAGS", "BODY[]")

	if err != nil {
		log.Fatalf("Fetch failed: %s", err)
	}

	for cmd.InProgress() {
		m.client.Recv(10 * time.Second)

		for _, rsp := range cmd.Data {
			body := imap.AsBytes(rsp.MessageInfo().Attrs["BODY[]"])
			
			if msg, _ := mail.ReadMessage(bytes.NewReader(body)); msg != nil {
				contentType, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
				if err != nil {
					log.Fatalf("Error parsing Content-Type: ", err)
				}
				
				fromAddr := strings.ToLower(msg.Header.Get("From"))
				re := regexp.MustCompile("<(.*)>")
				matches := re.FindStringSubmatch(fromAddr)
				if len(matches) > 1 {
					fromAddr = matches[1]
				}
				
				log.Printf("|-- Subject: %v", msg.Header.Get("Subject"))
				log.Printf("|-- From: %v", fromAddr)
				
				// if this email is from a valid poster
				if m.config.PostFrom == "" ||
					strings.ToLower(m.config.PostFrom) == fromAddr {
						
					// check mime parts for valid content
					if m.HasMultipart(contentType) {
						m.ExtractAttachment(msg.Body, params)
						
					// otherwise, save the plaintext email
					} else if m.HasText(contentType) {
						reader := quotedprintable.NewDecoder(msg.Body)
						if b, err := ioutil.ReadAll(reader); err == nil {
							m.ExtractPostData(string(b))
						}
					}
				}
			}
		}
		cmd.Data = nil
	}

	if rsp, err := cmd.Result(imap.OK); err != nil {
		if err == imap.ErrAborted {
			log.Fatal("Fetch command aborted")
		} else {
			log.Fatalf("Fetch error: %v", rsp.Info)
		}
	}

	log.Print("Marking messages seen..\n")
	cmd, err = m.client.UIDStore(set, "+FLAGS.SILENT",
		imap.NewFlagSet(`\Seen`))

	if rsp, err := cmd.Result(imap.OK); err != nil {
		log.Fatalf("UIDStore error:%v", rsp.Info)
	}

	cmd.Data = nil
}

func (m *Mailpost) HasImage(contentType string) bool {
	if strings.HasPrefix(contentType, "image/jpeg") ||
		strings.HasPrefix(contentType, "image/png") {
		return true
	}
	return false
}

func (m *Mailpost) HasText(contentType string) bool {
	if strings.HasPrefix(contentType, "text/plain") ||
		strings.HasPrefix(contentType, "multipart/alternative") {
		return true
	}
	return false
}

func (m *Mailpost) HasMultipart(contentType string) bool {
	if strings.HasPrefix(contentType, "multipart/") {
		return true
	}
	return false
}

func (m *Mailpost) OpenLog(path string) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("Error opening logfile: %v", err)
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
}

func (m *Mailpost) ReadConfig(path string) {
	if _, err := os.Stat(path); err != nil {
		log.Fatalf("File doesn't exist: %v", err)
	}

	if _, err := toml.DecodeFile(path, &m.config); err != nil {
		log.Fatalf("Error opening config file: %s", err)
	}
}

func (m *Mailpost) ExtractImageData(imageInfo Image) {
	// sanitize orig name and replace extension (we will save it as a jpg)
	imageInfo.Name = m.SanitizeFilename(imageInfo.OrigName)
    extension := filepath.Ext(imageInfo.Name)
	imageInfo.Name = imageInfo.Name[0:len(imageInfo.Name)-len(extension)]
	imageInfo.Name = imageInfo.Name + ".jpg"
	
	m.images = append(m.images, imageInfo)
}

func (imageInfo *Image) SaveImage(m *Mailpost, relatedPost Post) {
	
	// save the new path for this image				
	var pathData PathParts
	pathData.Date = m.MakeDatePathPart(relatedPost.Date)
	imageInfo.Path = m.MakePathFromTemplate(m.config.ImageDir, pathData)
	
	err := os.MkdirAll(imageInfo.Path, 0755)
	if err != nil {
		log.Fatalf("Couldn't make image path: %s", err)
	}
	
	imageInfo.Path = filepath.Join(imageInfo.Path, imageInfo.Name)
	
	// save the new URL for this image
	imageInfo.URL = filepath.Join(m.config.BaseURL, m.config.ImagePath, pathData.Date, imageInfo.Name)
	
	// load the image into memory
	imgReader := bytes.NewReader(imageInfo.Data)
	img, _, err := image.Decode(imgReader)
	if err != nil {
		log.Printf("Failed to decode image: %s", err)
	}
				
	// resize the image to max width specified in MaxImgWidth in the config file
	bounds := img.Bounds()
	width := uint(bounds.Max.X - bounds.Min.X)
			
	if width > m.config.MaxImgWidth {
		img = resize.Resize(m.config.MaxImgWidth, 0, img, resize.Lanczos3)
	}
			
	// add a white background in case there was transparency
	backgroundColor := color.RGBA{0xff, 0xff, 0xff, 0xff}
	finalImg := image.NewRGBA(img.Bounds())
	draw.Draw(finalImg, finalImg.Bounds(), image.NewUniform(backgroundColor), image.Point{}, draw.Src)
	draw.Draw(finalImg, finalImg.Bounds(), img, img.Bounds().Min, draw.Over)
						
	// save the image as a jpg
	outfile, err := os.Create(imageInfo.Path)
	if err != nil {
		log.Fatalf("Failed to output image file: %s", err)
	}
	defer outfile.Close()
			
	jpeg.Encode(outfile, finalImg, &jpeg.Options{jpeg.DefaultQuality})
	
	log.Printf("   |-- Saved image: %s", imageInfo.Path)
}

func (m *Mailpost) ExtractPostData(post string) {
	var postInfo Post

	postInfo.Data = post
	
	type T struct {
		Title string `yaml:"title"`
		Date string `yaml:"date"`
		Type string `yaml:"type"`
	}
	
	var t T
	err := yaml.Unmarshal([]byte(post), &t)
	if err != nil {
		log.Printf("Couldn't find post title in frontmatter. Skipping...")
		return
	}
	
	postInfo.Title = t.Title
	postInfo.Date = t.Date
	postInfo.Type = strings.ToLower(t.Type)
	
	postInfo.File = m.SanitizeFilename(t.Title) + ".md"
	
	postInfo.Path = m.config.PostDir
	postInfo.Path = m.MakePostPath(postInfo)
	
	m.posts = append(m.posts, postInfo)
}

func (m *Mailpost) WritePostToFile(postInfo Post) {
	path := filepath.Join(postInfo.Path, postInfo.File)
		
	dst, err := os.Create(path)
	if err != nil {
		log.Fatalf("Failed to create file: %s", err)
	}
	
	buf := bytes.NewBufferString(postInfo.Data)
	_, err = io.Copy(dst, buf)
	if err != nil {
		log.Fatalf("Failed to write post to file: %s", err)
	}
	
	log.Printf("   |-- Saved post: %s", path)
}

func (m *Mailpost) RetrieveImages() {
	var imageInfo Image
	
	re := regexp.MustCompile(`!\[.*\]\(\s*(https{0,1}://.*?)[\s|\)]`)
	for p:=0;p<len(m.posts);p++ {
		imageURLs := re.FindAllStringSubmatch(m.posts[p].Data, -1)
		
		for i:=0;i<len(imageURLs);i++ {
			log.Printf(">>>>> %v", imageURLs[i])
		    reqImg, err := http.Get(imageURLs[i][1])
		    if err != nil || reqImg.StatusCode != 200 {
		        log.Printf("Error %d, Status: %d", err, reqImg.StatusCode)
		        return
		    }
		    
		    imageInfo.Data, err = ioutil.ReadAll(reqImg.Body)
		    
		    defer reqImg.Body.Close()
			
			imageInfo.OrigURL = imageURLs[i][1]
			u, _ := url.Parse(imageInfo.OrigURL)
			imageInfo.OrigName = filepath.Base(u.Path)
						
			m.ExtractImageData(imageInfo)
		}
	}
}

func (m *Mailpost) ReplaceImageRefs() {
	reMdImg := regexp.MustCompile(`!\[.*\]\(\s*(.*?)[\s|\)]`)
	reScFig := regexp.MustCompile(`{{<\s*figure.*src="(.*?)"`)
	reScImg := regexp.MustCompile(`{{<\s*img.*src="(.*?)"`)

	for p:=0;p<len(m.posts);p++ {
		mdImgMatches := reMdImg.FindAllStringSubmatch(m.posts[p].Data, -1)
		scFigMatches := reScFig.FindAllStringSubmatch(m.posts[p].Data, -1)
		scImgMatches := reScImg.FindAllStringSubmatch(m.posts[p].Data, -1)
		
		for i:=0;i<len(mdImgMatches);i++ {
			for j:=0;j<len(m.images);j++ {
				if m.images[j].OrigName==mdImgMatches[i][1] ||					
					m.images[j].OrigURL==mdImgMatches[i][1] {		
								
					m.images[j].SaveImage(m, m.posts[p])
					m.posts[p].Data = strings.Replace(m.posts[p].Data, mdImgMatches[i][1], m.images[j].URL, 1)
				}
			}
		}
		for i:=0;i<len(scFigMatches);i++ {
			for j:=0;j<len(m.images);j++ {
				if m.images[j].OrigName==scFigMatches[i][1] ||					
					m.images[j].OrigURL==scFigMatches[i][1] {		
								
					m.images[j].SaveImage(m, m.posts[p])
					m.posts[p].Data = strings.Replace(m.posts[p].Data, scFigMatches[i][1], m.images[j].URL, 1)
				}
			}
		}
		for i:=0;i<len(scImgMatches);i++ {
			for j:=0;j<len(m.images);j++ {
				if m.images[j].OrigName==scImgMatches[i][1] ||					
					m.images[j].OrigURL==scImgMatches[i][1] {		
								
					m.images[j].SaveImage(m, m.posts[p])
					m.posts[p].Data = strings.Replace(m.posts[p].Data, scImgMatches[i][1], m.images[j].URL, 1)
				}
			}
		}
		m.WritePostToFile(m.posts[p])
	}
}

func main() {
	flag.Parse()

	if *debug {
		imap.DefaultLogger = log.New(os.Stdout, "", 0)
		imap.DefaultLogMask = imap.LogConn | imap.LogRaw
	}

	m := Mailpost{}
	m.ReadConfig(*conf)
	m.OpenLog(*logfile)

	for {
		m.Connect()
		m.FetchMails()
		m.RetrieveImages()
		m.ReplaceImageRefs()
		m.client.Logout(1 * time.Second)

		if *once {
			os.Exit(0)
		} else {
			t, _ := time.ParseDuration(*interval)
			log.Printf("Waiting for %v", t)
			time.Sleep(t)
		}
	}	
}