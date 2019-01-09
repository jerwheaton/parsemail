package parsemail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
)

const (
	contentTypeMultipartMixed       = "multipart/mixed"
	contentTypeMultipartAlternative = "multipart/alternative"
	contentTypeMultipartRelated     = "multipart/related"
	contentTypeTextHtml             = "text/html"
	contentTypeTextPlain            = "text/plain"

	encoding7bit            = "7bit"
	encoding8Bit            = "8bit"
	encodingBase64          = "base64"
	encodingBinary          = "binary"
	encodingQuotedPrintable = "quoted-printable"
	encodingEmpty           = ""

	headerContentType     = "Content-Type"
	headerContentEncoding = "Content-Transfer-Encoding"
)

func decodeBodyPart(part io.Reader, encoding string) (string, error) {
	switch encoding {
	case encodingBase64:
		pbytes, err := ioutil.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
		return string(pbytes), err
	case encodingQuotedPrintable:
		d, err := ioutil.ReadAll(quotedprintable.NewReader(part))
		return string(d), err
	case encoding7bit, encoding8Bit, encodingBinary, encodingEmpty:
		pbytes, err := ioutil.ReadAll(part)
		return string(pbytes), err
	default:
		return "", fmt.Errorf("Unrecognized content encoding")
	}
}

func addToTextBody(e *Email, decoded string) {
	trimmed := strings.TrimSuffix(string(decoded[:]), "\n")
	e.TextBody += trimmed
	e.TextBodyParts = append(e.TextBodyParts, trimmed)
}

func addToHTMLBody(e *Email, decoded string) {
	trimmed := strings.TrimSuffix(string(decoded[:]), "\n")
	e.HTMLBody += trimmed
	e.HTMLBodyParts = append(e.HTMLBodyParts, trimmed)
}

// Parse an email message read from io.Reader into parsemail.Email struct
func Parse(r io.Reader) (email Email, err error) {
	msg, err := mail.ReadMessage(r)
	if err != nil {
		return
	}

	email, err = createEmailFromHeader(msg.Header)
	if err != nil {
		return
	}

	contentType, params, err := parseContentType(msg.Header.Get(headerContentType))
	if err != nil {
		return
	}

	switch contentType {
	case contentTypeMultipartMixed:
		err = parseMultipartMixed(&email, msg.Body, params["boundary"])
	case contentTypeMultipartRelated:
		err = parseMultipartRelated(&email, msg.Body, params["boundary"])
	case contentTypeMultipartAlternative:
		err = parseMultipartAlternative(&email, msg.Body, params["boundary"])
	case contentTypeTextPlain:
		message, decodeErr := decodeBodyPart(msg.Body, msg.Header.Get(headerContentEncoding))
		if err != nil {
			err = decodeErr
			return
		}
		addToTextBody(&email, message)
	case contentTypeTextHtml:
		message, decodeErr := decodeBodyPart(msg.Body, msg.Header.Get(headerContentEncoding))
		if err != nil {
			err = decodeErr
			return
		}
		addToHTMLBody(&email, message)
	default:
		err = fmt.Errorf("Unknown top level mime type: %s", contentType)
	}

	return
}

func createEmailFromHeader(header mail.Header) (email Email, err error) {
	hp := headerParser{header: &header}

	email.Subject = decodeMimeSentence(header.Get("Subject"))
	email.From = hp.parseAddressList(header.Get("From"))
	email.Sender = hp.parseAddress(header.Get("Sender"))
	email.ReplyTo = hp.parseAddressList(header.Get("Reply-To"))
	email.To = hp.parseAddressList(header.Get("To"))
	email.Cc = hp.parseAddressList(header.Get("Cc"))
	email.Bcc = hp.parseAddressList(header.Get("Bcc"))
	email.Date = hp.parseTime(header.Get("Date"))
	email.ResentFrom = hp.parseAddressList(header.Get("Resent-From"))
	email.ResentSender = hp.parseAddress(header.Get("Resent-Sender"))
	email.ResentTo = hp.parseAddressList(header.Get("Resent-To"))
	email.ResentCc = hp.parseAddressList(header.Get("Resent-Cc"))
	email.ResentBcc = hp.parseAddressList(header.Get("Resent-Bcc"))
	email.ResentMessageID = hp.parseMessageId(header.Get("Resent-Message-ID"))
	email.MessageID = hp.parseMessageId(header.Get("Message-ID"))
	email.InReplyTo = hp.parseMessageIdList(header.Get("In-Reply-To"))
	email.References = hp.parseMessageIdList(header.Get("References"))
	email.ResentDate = hp.parseTime(header.Get("Resent-Date"))

	if hp.err != nil {
		err = hp.err
		return
	}

	//decode whole header for easier access to extra fields
	//todo: should we decode? aren't only standard fields mime encoded?
	email.Header, err = decodeHeaderMime(header)
	if err != nil {
		return
	}

	return
}

func parseContentType(contentTypeHeader string) (contentType string, params map[string]string, err error) {
	if contentTypeHeader == "" {
		contentType = contentTypeTextPlain
		return
	}

	return mime.ParseMediaType(contentTypeHeader)
}

func parseMultipartRelated(e *Email, msg io.Reader, boundary string) error {
	pmr := multipart.NewReader(msg, boundary)
	for {
		part, err := pmr.NextPart()

		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}

		contentType, params, err := mime.ParseMediaType(part.Header.Get(headerContentType))
		if err != nil {
			return err
		}

		switch contentType {
		case contentTypeTextPlain:
			ppContent, err := decodeBodyPart(part, part.Header.Get(headerContentEncoding))
			if err != nil {
				return err
			}

			addToTextBody(e, ppContent)
		case contentTypeTextHtml:
			ppContent, err := decodeBodyPart(part, part.Header.Get(headerContentEncoding))
			if err != nil {
				return err
			}

			addToHTMLBody(e, ppContent)
		case contentTypeMultipartAlternative:
			if err := parseMultipartAlternative(e, part, params["boundary"]); err != nil {
				return err
			}
		default:
			if isEmbeddedFile(part) {
				ef, err := decodeEmbeddedFile(part)
				if err != nil {
					return err
				}

				e.EmbeddedFiles = append(e.EmbeddedFiles, ef)
			} else {
				return fmt.Errorf("Can't process multipart/related inner mime type: %s", contentType)
			}
		}
	}

	return nil
}

func parseMultipartAlternative(e *Email, msg io.Reader, boundary string) error {
	pmr := multipart.NewReader(msg, boundary)
	for {
		part, err := pmr.NextPart()

		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}

		contentType, params, err := mime.ParseMediaType(part.Header.Get(headerContentType))
		if err != nil {
			return err
		}

		switch contentType {
		case contentTypeTextPlain:
			ppContent, err := decodeBodyPart(part, part.Header.Get(headerContentEncoding))
			if err != nil {
				return err
			}

			addToTextBody(e, ppContent)
		case contentTypeTextHtml:
			ppContent, err := decodeBodyPart(part, part.Header.Get(headerContentEncoding))
			if err != nil {
				return err
			}

			addToHTMLBody(e, ppContent)
		case contentTypeMultipartRelated:
			if err := parseMultipartRelated(e, part, params["boundary"]); err != nil {
				return err
			}
		default:
			if isEmbeddedFile(part) {
				ef, err := decodeEmbeddedFile(part)
				if err != nil {
					return err
				}

				e.EmbeddedFiles = append(e.EmbeddedFiles, ef)
			} else {
				return fmt.Errorf("Can't process multipart/alternative inner mime type: %s", contentType)
			}
		}
	}

	return nil
}

func parseMultipartMixed(e *Email, msg io.Reader, boundary string) error {
	mr := multipart.NewReader(msg, boundary)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}

		contentType, params, err := mime.ParseMediaType(part.Header.Get(headerContentType))
		if err != nil {
			return err
		}

		if contentType == contentTypeMultipartAlternative {
			if err = parseMultipartAlternative(e, part, params["boundary"]); err != nil {
				return err
			}
		} else if contentType == contentTypeMultipartRelated {
			if err = parseMultipartRelated(e, part, params["boundary"]); err != nil {
				return err
			}
		} else if isAttachment(part) {
			at, err := decodeAttachment(part)
			if err != nil {
				return err
			}

			e.Attachments = append(e.Attachments, at)
		} else {
			return fmt.Errorf("Unknown multipart/mixed nested mime type: %s", contentType)
		}
	}

	return nil
}

func decodeMimeSentence(s string) string {
	result := []string{}
	ss := strings.Split(s, " ")

	for _, word := range ss {
		dec := new(mime.WordDecoder)
		w, err := dec.Decode(word)
		if err != nil {
			if len(result) == 0 {
				w = word
			} else {
				w = " " + word
			}
		}

		result = append(result, w)
	}

	return strings.Join(result, "")
}

func decodeHeaderMime(header mail.Header) (mail.Header, error) {
	parsedHeader := map[string][]string{}

	for headerName, headerData := range header {

		parsedHeaderData := []string{}
		for _, headerValue := range headerData {
			parsedHeaderData = append(parsedHeaderData, decodeMimeSentence(headerValue))
		}

		parsedHeader[headerName] = parsedHeaderData
	}

	return mail.Header(parsedHeader), nil
}

func decodePartData(part *multipart.Part) (io.Reader, error) {
	encoding := part.Header.Get(headerContentEncoding)

	if strings.EqualFold(encoding, "base64") {
		dr := base64.NewDecoder(base64.StdEncoding, part)
		dd, err := ioutil.ReadAll(dr)
		if err != nil {
			return nil, err
		}

		return bytes.NewReader(dd), nil
	}

	return nil, fmt.Errorf("Unknown encoding: %s", encoding)
}

func isEmbeddedFile(part *multipart.Part) bool {
	return strings.Contains(part.Header.Get("Content-Disposition"), "attachment") ||
		strings.HasPrefix(part.Header.Get("Content-Type"), "image/")
}

func decodeEmbeddedFile(part *multipart.Part) (ef EmbeddedFile, err error) {
	cid := decodeMimeSentence(part.Header.Get("Content-Id"))
	decoded, err := decodePartData(part)
	if err != nil {
		return
	}

	ef.CID = strings.Trim(cid, "<>")
	ef.Data = decoded
	ef.ContentType = part.Header.Get(headerContentType)

	return
}

func isAttachment(part *multipart.Part) bool {
	return part.FileName() != ""
}

func decodeAttachment(part *multipart.Part) (at Attachment, err error) {
	filename := decodeMimeSentence(part.FileName())
	decoded, err := decodePartData(part)
	if err != nil {
		return
	}

	at.Filename = filename
	at.Data = decoded
	at.ContentType = strings.Split(part.Header.Get(headerContentType), ";")[0]

	return
}

type headerParser struct {
	header *mail.Header
	err    error
}

func (hp headerParser) parseAddress(s string) (ma *mail.Address) {
	if hp.err != nil {
		return nil
	}

	if strings.Trim(s, " \n") != "" {
		ma, hp.err = mail.ParseAddress(s)

		return ma
	}

	return nil
}

func (hp headerParser) parseAddressList(s string) (ma []*mail.Address) {
	if hp.err != nil {
		return
	}

	if strings.Trim(s, " \n") != "" {
		ma, hp.err = mail.ParseAddressList(s)
		return
	}

	return
}

func (hp headerParser) parseTime(s string) (t time.Time) {
	if hp.err != nil || s == "" {
		return
	}

	t, hp.err = time.Parse(time.RFC1123Z, s)
	if hp.err == nil {
		return t
	}

	t, hp.err = time.Parse("Mon, 2 Jan 2006 15:04:05 -0700", s)

	return
}

func (hp headerParser) parseMessageId(s string) string {
	if hp.err != nil {
		return ""
	}

	return strings.Trim(s, "<> ")
}

func (hp headerParser) parseMessageIdList(s string) (result []string) {
	if hp.err != nil {
		return
	}

	for _, p := range strings.Split(s, " ") {
		if strings.Trim(p, " \n") != "" {
			result = append(result, hp.parseMessageId(p))
		}
	}

	return
}

// Attachment with filename, content type and data (as a io.Reader)
type Attachment struct {
	Filename    string
	ContentType string
	Data        io.Reader
}

// EmbeddedFile with content id, content type and data (as a io.Reader)
type EmbeddedFile struct {
	CID         string
	ContentType string
	Data        io.Reader
}

// Email with fields for all the headers defined in RFC5322 with it's attachments and
type Email struct {
	Header mail.Header

	Subject    string
	Sender     *mail.Address
	From       []*mail.Address
	ReplyTo    []*mail.Address
	To         []*mail.Address
	Cc         []*mail.Address
	Bcc        []*mail.Address
	Date       time.Time
	MessageID  string
	InReplyTo  []string
	References []string

	ResentFrom      []*mail.Address
	ResentSender    *mail.Address
	ResentTo        []*mail.Address
	ResentDate      time.Time
	ResentCc        []*mail.Address
	ResentBcc       []*mail.Address
	ResentMessageID string

	HTMLBody string
	TextBody string

	TextBodyParts []string
	HTMLBodyParts []string

	Attachments   []Attachment
	EmbeddedFiles []EmbeddedFile
}
