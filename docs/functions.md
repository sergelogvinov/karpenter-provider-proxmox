# Template function list

The Cloud-Init provider supports several template functions that can be used to customize the generated Cloud-Init configuration files.

### Logic functions

* `default` - the function to return the default value if the given value is empty.

  ```yaml
  {{ default "world" "" }} -> world
  ```

  ```yaml
  {{ default "world" "hello" }} -> hello
  ```
* `coalesce` - the function to return the first non-empty value from the list of values.

  ```yaml
  {{ coalesce "" "" "hello" "world" }} -> hello
  ```

* `ternary` - the function to return one of the two values based on the condition.

  ```yaml
  {{ ternary "yes" "no" true }} -> yes
  ```

  ```yaml
  {{ ternary "yes" "no" false }} -> no
  ```

* `toJson` - the function to convert the value to a JSON string.

* `toPrettyJson` - the function to convert the value to a pretty-printed JSON string.

* `toYaml` - the function to convert the value to a YAML string.

* `toPrettyYaml` - the function to convert the value to a pretty-printed YAML string.

### String modification functions

* `indent` - the function to indent each line of the string with the specified number of spaces.

  ```yaml
  {{ indent 4 "hello\nworld" }} -> "    hello\n    world"
  ```

* `nindent` - the function to indent each line of the string with the specified number of spaces, including a newline at the beginning.

  ```yaml
  {{ nindent 4 "hello\nworld" }} -> "\n    hello\n    world"
  ```

* `upper` - the function to convert the string to uppercase.

  ```yaml
  {{ upper "hello" }} -> HELLO
  ```

* `lower` - the function to convert the string to lowercase.

  ```yaml
  {{ lower "HELLO" }} -> hello
  ```

* `trim` - the function to remove leading and trailing whitespace from the string.

  ```yaml
  {{ trim "  hello  " }} -> hello
  ```

* `trimSuffix` - the function to remove the suffix from the string.

  ```yaml
  {{ trimSuffix "hello" "lo" }} -> hel
  ```

* `trimPrefix` - the function to remove the prefix from the string.

  ```yaml
  {{ trimPrefix "hello" "he" }} -> llo
  ```

* `replace` - the function to replace all occurrences of the old string with the new string.

  ```yaml
  {{ replace "hello" "l" "L" }} -> heLLo
  ```

* `regexFind` - return the first (left most) match of the regular expression in the input string.

  ```yaml
  {{ regexFind "[a-zA-Z][1-9]" "abcd1234" }} -> d1
  ```

* `regexFindString` - the function to find the match of the regular expression pattern in the string and return the submatch at the specified index.

  ```yaml
  {{ regexFindString "^type-([a-z0-9]+)-(.*)$" "type-metal1-asz" 1 }} -> metal1
  ```

* `regexReplaceAll` - the function to replace all occurrences of the regular expression in the input string with the replacement string.

  ```yaml
  {{ regexReplaceAll "a(x*)b" "-ab-axxb-" "${1}W" }} -> -W-xxW-
  ```


### Conditional functions

* `empty` - the function to return true if the string is empty.

  ```yaml
  {{ empty "" }} -> true
  ```

* `contains` - the function to return true if the string contains the substring.

  ```yaml
  {{ contains "hello" "lo" }} -> true
  ```

* `hasPrefix` - the function to return true if the string has the specified prefix.

  ```yaml
  {{ hasPrefix "hello" "he" }} -> true
  ```

* `hasSuffix` - the function to return true if the string has the specified suffix.

  ```yaml
  {{ hasSuffix "hello" "lo" }} -> true
  ```

* `hasKey` - the function to return true if the map has the specified key.

  ```yaml
  {{ hasKey (dict "a" 1 "b" 2) "a" }} -> true
  ```

### Encoding functions

* `b64enc` - the function to return the base64-encoded string.

  ```yaml
  {{ b64enc "hello" }} -> aGVsbG8=
  ```
* `b64dec` - the function to return the base64-decoded string.

  ```yaml
  {{ b64dec "aGVsbG8=" }} -> hello
  ```

### String slice functions

* `getValue` - the function to get the value from the map by key.

  ```yaml
  {{ getValue "ds=nocloud;i=1234" "i" }} -> 1234
  ```


### Network functions

* `cidrhost` - the function to return the IP address of the host in the CIDR notation.

  ```yaml
  {{ cidrhost "192.168.1.5/24" }} -> 192.168.1.5
  ```

  ```yaml
  {{ cidrhost "192.168.1.5/24" 15 }} -> 192.168.1.15
  ```

* `cidrslaac` - the function to return the SLAAC address for the given CIDR and MAC address.

  ```yaml
  {{ "2001:db8::/64" | cidrslaac "00:1A:2B:3C:4D:5E" }} -> 2001:db8:1:0:21a:2bff:fe3c:4d5e/64
  ```

  ```yaml
  {{ "2001:db8:1:2:3::/80" | cidrslaac "00:1A:2B:3C:4D:5E" }} -> 2001:db8:1:2:3:2bff:fe3c:4d5e/80
  ```

## References

For more details on the functions, see the [source code](/pkg/providers/instance/cloudinit/functions.go).

Those functions was inspired by Helm [Template Functions and Pipelines](https://helm.sh/docs/chart_template_guide/functions_and_pipelines/).
