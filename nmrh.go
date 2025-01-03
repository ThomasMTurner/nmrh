package main

/*
TO DO:
- Implement PDF parsing for arxiv_pdf.
- Determine any other possible entry errors (i.e. not passing any cmd arguments).
- Build binary and release on Homebrew (and possibly other) package managers.
*/

import (
    "flag"
    "fmt"
    "strings"
    "net/url"
    "net/http"
    "time"
    "github.com/gocolly/colly"
    "sync"
    "github.com/jdkato/prose/v2"
    "github.com/fatih/color"
    "io"
    "os"
    "github.com/unidoc/unipdf/v3/model"
    "github.com/unidoc/unipdf/v3/extractor"
    "github.com/google/uuid"
    "path/filepath"
)

type ReadingResource struct {
    ResourceType byte
    RawURL string
    ValidatedURL url.URL
    RawContent []string
    TokenisedContent string
    title string
    WordCount int
    ReadTime time.Duration
}

func NewReadingResource(resourceType byte, url string) (*ReadingResource, error) {
    resource := &ReadingResource {
        ResourceType: resourceType,
        RawURL: url,
    }
    check := ValidateResource(resource)
    return resource, check
}

// Byte cases are given as;
// 0 -> blog
// 1 -> substack
// 2 -> arxiv_pdf
// 3 -> arxiv_html

func ValidateResource(r *ReadingResource) error {
    u, err := url.ParseRequestURI(r.RawURL)
    if err != nil {
        return &ValidateError{Message: err.Error()}
    }

    host := u.Hostname()
    path := u.Path
    
    switch r.ResourceType {
        case 0:
            // No action needed for blog. 

        case 1:
            if strings.Contains(path, "/post") && strings.Contains(host, "substack.com") {
                r.ValidatedURL = *u
            } else {
                return &ValidateError{Message: "Invalid Substack URL."}
            }
            
        case 2:
            if strings.Contains(path, "pdf") && host == "arxiv.org" {
                r.ValidatedURL = *u
            } else {
                return err
            }

        case 3:
            if strings.Contains(path, "html") && host == "arxiv.org" {
                r.ValidatedURL = *u
            } else {
                return err
            }
        
        // Immediately return error (incorrect type provided).
        default:
            return &ValidateError{Message: "Invalid resource type."}
    }

    return nil
}

func DownloadPDF(url string, destinationFolder string) error {

    // Check what to place in format method.
    timestamp := time.Now().Format("20060102_150405")
    id := uuid.New().String()[:8]
    filename := fmt.Sprintf("%s_%s.pdf", id, timestamp)
    
    destination := filepath.Join(destinationFolder, filename)
    
    response, err := http.Get(url)
    if err != nil {
        return err
    }
    defer response.Body.Close()

    out, err := os.Create(destination)
    if err != nil {
        return err
    }
    defer out.Close()

    _, err = io.Copy(out, response.Body)
    return err
}

func ScrapeResource(r *ReadingResource) error {

    c := colly.NewCollector()
    var callbackErr error
    
    // Register error event handler.
    c.OnError(func(r *colly.Response, err error) {
        callbackErr = err
    })

    var contentSlice []string
    var title string

    c.OnHTML("title, h1.post-title, h1.article-title", func(e *colly.HTMLElement) {
        title = e.Text
    })

    switch r.ResourceType {
        case 0:
            c.OnHTML("h1, p, span", func(e *colly.HTMLElement) {
                contentSlice = append(contentSlice, e.Text)
            })

        case 1, 3:
            c.OnHTML("p", func(e *colly.HTMLElement) {
                contentSlice = append(contentSlice, e.Text)
            })

        case 2:
            // Extract PDF from the URL.
            // Testing with "tmp.pdf" temporary file
            // Will place in batch folder...
            err := os.MkdirAll("arxiv", os.ModePerm)
            if err != nil {
                return err
            }

            err = DownloadPDF(r.ValidatedURL.String(), "arxiv")
            if err != nil {
                return &ParseError{Message: err.Error()}
            }

            // Parse out text with unipdf (testing with single file).
            // Further, these panics will be replaced by error accumulation.

            entries, err := os.ReadDir("arxiv")
            if err != nil {
                return &ParseError{Message: err.Error()}
            }
            if len(entries) == 0 {
                return &ParseError{Message: "No files found in arxiv folder."}
            }

            for _, entry := range entries {
                if entry.IsDir() {
                    continue
                }
                filename := entry.Name()
                if filepath.Ext(filename) == ".pdf" {
                    f, err := os.Open(filepath.Join("arxiv", filename))
                    if err != nil {
                        return &ParseError{Message: err.Error()}
                    }
                    defer f.Close()
                    pdfReader, err := model.NewPdfReader(f)
                    if err != nil {
                        return &ParseError{Message: err.Error()}
                    }
                    numPages, err := pdfReader.GetNumPages()
                    if err != nil {
                        return &ParseError{Message: err.Error()}
                    }

                    for i := range numPages {
                        page, err := pdfReader.GetPage(i + 1)
                        if err != nil {
                            return &ParseError{Message: err.Error()}
                        }
                        ex, err := extractor.New(page)
                        if err != nil {
                            return &ParseError{Message: err.Error()}
                        }
                        text, err := ex.ExtractText()
                        if err != nil {
                            return &ParseError{Message: err.Error()}
                        }
                        contentSlice = append(contentSlice, text)
                    }  
                }
            } 

        default:
            return &ParseError{Message: "Invalid resource type."}
    }

    visitErr := c.Visit(r.ValidatedURL.String())

    // We have implicitly prioritised visitation errors over callback errors...
    if visitErr != nil {
        return visitErr
    }

    if callbackErr != nil {
        return callbackErr
    }

   
    r.RawContent = contentSlice
    r.title = title

    return nil
}

// For this we will need to complete some extensive research...
func GetComplexityAndReadingTimeEstimate (doc *prose.Document, wpm int) (float64, float64, error) {
    var sentenceCount, tokenCount, wordCount int

    sentenceCount = len(doc.Sentences())
    tokenCount = len(doc.Tokens())
    wordCount = int(float64(tokenCount) * 0.75)

    depth := 0
    maxSyntacticDepth := 0

    for _, token := range doc.Tokens() {
        tag := token.Tag
        if strings.HasPrefix(tag, "VB") || tag == "IN" || tag == "WDT" || tag == "WP" || tag == "WRB" {
            depth++
        } else if tag == "." || tag == "!" || tag == "?" {
            if depth > maxSyntacticDepth {
                maxSyntacticDepth = depth
            }
            depth = 0
        }
    }
    
    // I also want to incorporate lexical complexity on the basis of individual words.
    averageSentenceLength := float64(wordCount) / float64(sentenceCount)
    var complexity float64

    if maxSyntacticDepth <= 4 {
        if averageSentenceLength <= 20 {
            complexity = 1.0
        } else {
            complexity = 1.5
        }

    } else {
        if averageSentenceLength <= 20 {
            complexity = 1.5
        } else {
            complexity = 2.0
        }
    }

    estimate := float64(tokenCount) / float64(wpm)
    
    return complexity, estimate, nil
}


func AnalyseResource(r *ReadingResource, wpm int) error {
    doc, err := prose.NewDocument(strings.Join(r.RawContent, " "))
    if err != nil {
        return err
    }
    
    switch r.ResourceType {
        case 0:
            // Extended analysis.

        case 1, 2, 3:
            complexity, estimate, err := GetComplexityAndReadingTimeEstimate(doc, wpm)
            if err != nil {
                return err
            }

            r.ReadTime = time.Duration(estimate * complexity) * time.Minute
            
            fmt.Println("\n")
            color.Green("Analysed resource %s", r.RawURL)
            fmt.Println("Complexity value: ", complexity)
            fmt.Println("Base estimate reading time: ", estimate, " minutes/seconds")
            fmt.Println("Scaled reading time: ", estimate * complexity, " minutes/seconds")

        default:
            return &AnalyseError{Message: "Invalid resource type."}
    }
    
    return nil
}

type ValidateError struct {
    Message string
}

func (e *ValidateError) Error() string {
    return fmt.Sprintf("Validation Error: %s", e.Message)
}

type ParseError struct {
    Message string
}

func (e *ParseError) Error() string {
    return fmt.Sprintf("Parse Error: %s", e.Message)
}

type AnalyseError struct {
    Message string
}

func (e *AnalyseError) Error() string {
    return fmt.Sprintf("Analyse Error: %s", e.Message)
}

type resourceList []ReadingResource

type urlList []string

// Use the fmt.Stringer interface to print a list of URLs.
func (url *urlList) String() string {
    return strings.Join(*url, ",")
}

// Use the flag.Value interface to set a list of URLs.
func (url *urlList) Set(value string) error {
    *url = append(*url, value)
    return nil
}

func main() {
    var blogs urlList
    var substackResources urlList
    var arxivHtmlResources urlList
    var arxivPdfResources urlList
    var resources resourceList
    var wpm int
    var errors []error

    parsedResources := make([]ReadingResource, 0, len(resources))

    flag.Var(&blogs, "blog", "List of blog resources")
    flag.Var(&substackResources, "substack", "List of Substack resources")
    flag.Var(&arxivHtmlResources, "arxiv_html", "List of experimental ArXiV HTML resources")
    flag.Var(&arxivPdfResources, "arxiv_pdf", "List of standard ArXiV PDF resources")
    flag.IntVar(&wpm, "wpm", 200, "Words per minute reading speed (WPM)")
    flag.Parse()

    // Now we should validate every URL for parseability.
    // Concurrency patterns are not used here as there are no I/O-bound operations
    // involved in URI validation.
    for _, b := range blogs {
        resource, check := NewReadingResource(0, b)
        if check != nil {
            errors = append(errors, check)
        }
        resources = append(resources, *resource)
    }

    for _, s := range substackResources {
        resource, check := NewReadingResource(1, s)
        if check != nil {
            errors = append(errors, check)
        }
        resources = append(resources, *resource)
    }

    for _, ap := range arxivPdfResources {
        resource, check := NewReadingResource(2, ap)
        if check != nil {
            errors = append(errors, check)
        }
        resources = append(resources, *resource)
    }

    for _, ah := range arxivHtmlResources {
        resource, check := NewReadingResource(3, ah)
        if check != nil {
            errors = append(errors, check)
        }
        resources = append(resources, *resource)
    }

    var wg sync.WaitGroup
    
    // Iterate over the resources
    for _, r := range resources {
        // Make a copy of r to avoid closure capture issues
        resourceCopy := r
        wg.Add(1)
    
        // Start a goroutine to scrape each resource
        go func(r ReadingResource) {  
            defer wg.Done()  
            parseErr := ScrapeResource(&resourceCopy)
            analyseErr := AnalyseResource(&resourceCopy, wpm)
            errors = append(errors, parseErr, analyseErr)
            parsedResources = append(parsedResources, resourceCopy)
    }(resourceCopy)  
}
    wg.Wait()

    fmt.Println("\n")

    // Return time-to-read.
    for _, resource := range parsedResources {
        color.Blue("It will take approximately %s to read %s\n", resource.ReadTime.String(), resource.RawURL)
    }


    color.Magenta("With a WPM reading speed of %d words per minute (scaled for textual complexity).", wpm)
    fmt.Println("\n")

    if len(errors) == 0 {
        color.Green("No errors detected when parsing resources!")
    }

    for _, error := range errors {
        color.Red("Obtained the following error(s) when parsing resources: %s", error)
    }

    fmt.Println("\n")
}



/*

Want to consider differentiating between resources.

So just some key examples to start with, we can collect more later:

    (a) Personal blogs (little prior information for parsing; may need NLP analysis).
        (note) When these are recognised, attempt P tag initially, apply some analysis
        to see if extracted content constitutes the majority of text content on the entire page.
    (b) ArXiV pre-prints (PDF form; information is in span tags, experimental HTML; in P tags).
    (c) Substack pages (content resides predominantly in P tags).
    
For the above, users should supply '-blog', '-arxiv_pdf', 'arxiv_html', '-substack' flags.
*/
