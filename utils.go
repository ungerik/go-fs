package fs

import (
	"encoding/json"
	"encoding/xml"
	"strings"
)

func ReadString(uri string) (string, error) {
	data, err := Read(uri)
	if data == nil || err != nil {
		return "", err
	}
	return string(data), nil
}

func WriteString(uri string, data string, perm ...Permissions) error {
	return Write(uri, []byte(data), perm...)
}

func AppendString(uri string, data string, perm ...Permissions) error {
	return Append(uri, []byte(data), perm...)
}

func ReadJSON(file File, output interface{}) error {
	data, err := file.ReadAll()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, output)
}

func WriteJSON(file File, input interface{}, indent ...string) error {
	b, err := json.MarshalIndent(input, "", strings.Join(indent, ""))
	if err != nil {
		return err
	}
	return file.WriteAll(b)
}

func ReadXML(file File, output interface{}) error {
	data, err := file.ReadAll()
	if err != nil {
		return err
	}
	return xml.Unmarshal(data, output)
}

func WriteXML(file File, input interface{}, indent ...string) error {
	b, err := xml.MarshalIndent(input, "", strings.Join(indent, ""))
	if err != nil {
		return err
	}
	b = append([]byte(xml.Header), b...)
	return file.WriteAll(b)
}
