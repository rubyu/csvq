# Change Log

## Version 1.11.4

Released on Jun 1, 2019

- Fix the following bug.
  - NOT IN operator returns incorrect result in some cases.

## Version 1.11.3

Released on Jun 1, 2019

- Implement FETCH clause that can be used in place of LIMIT clause in SELECT query.
- Fix the following bug.
  - FALSE takes precedence over UNKNOWN in NOT IN operation.

## Version 1.11.2

Released on May 26, 2019

- Add built-in functions.
  - Aggregate Function
    - STDEV
    - STDEVP
    - VAR
    - VARP
  - Analytic Function
    - STDEV
    - STDEVP
    - VAR
    - VARP

## Version 1.11.1

Released on May 19, 2019

- Fix the following bugs.
  - Subqueries in recursive queries cannot reference that temporary tables.

## Version 1.11.0

Released on May 19, 2019

- Enable enclosure characters to be escaped with double enclosures.
- Add the command option "--ansi-quotes".
- Add the Flag "@@ANSI_QUOTES".
- Fix the following bugs.
  - Double backspaches in a string cannot be parsed correctly.
  - Escaping in external command syntax cannot be parsed correctly.
  - Versions are not compared correctly in check-update subcommand.

## Version 1.10.6

Released on May 4, 2019

- Add subcommand option "--include-pre-release" to check-update subcommand.

## Version 1.10.4

Released on May 3, 2019

- Fix the following bug.
  - Panic occurrs when a subquery in comparison returns empty set.

## Version 1.10.3

Released on May 3, 2019

- Add the following built-in funciton.
  - NANO_TO_DATETIME
- Fix the following bug.
  - Timezone not set correctly on second change.
- Disable automatic type conversion from number to datetime.
- Improve execusion performance.

## Version 1.10.1

Released on April 28, 2019

- Improve execusion performance.

## Version 1.10.0

Released on April 25, 2019

- Support UTF-16.
- Support automatic detection of character encoding.
- Remove the use of glide from support.

## Version 1.9.19

Released on April 22, 2019

- Improve execusion performance.

## Version 1.9.18

Released on April 20, 2019

- Change the settings of Limit-Recursion from configuration files to the command option.
  - Remove "limit_recursion" from csvq_env.json.
  - Add the command option "--limit-recursion". 
  - Add the Flag "@@LIMIT_RECURSION".

## Version 1.9.17

Released on April 20, 2019

- Set the limit of iterations for recursive queries.

  The limit can be changed in [configuration json file](https://mithrandie.github.io/csvq/reference/command.html#configurations).
- Fix the following bug.
  - The global options are ignored by subcommands.

## Version 1.9.16

Released on April 16, 2019

- Fix the following bug.
  - Reference scopes cannot be refered from subquery. This bug has occurred in version 1.9.15.

## Version 1.9.15

Released on April 16, 2019

- Fix the following bug.
  - COUNT(null) returns the number of rows, but must return 0.
- Remove "Alloc" from resource statistics.
- Optimize memory usage in JOIN operation.
  In most cases, the execusion performance gets better, and in a few cases, it gets a little worse.

## Version 1.9.14

Released on April 14, 2019

- Support SELECT INTO Variables statement.

## Version 1.9.13

Released on April 13, 2019

- Support REPLACE (Insert or Update) query.
- Allow variable declaration in WHILE IN statement in user defined functions.

## Version 1.9.12

Released on April 11, 2019

- Fix the following bug.
  - Compound field values containing aggregate functions do not return the results if there are no rows in the result set.

## Version 1.9.11

Released on April 11, 2019

- Allow flags to be used as values.
- Fix the following bug.
  - Aggregate functions do not return the results if there are no rows in the result set.

## Version 1.9.10

Released on April 10, 2019

- Make stdin table available for use in table object expressions.
- Enable dispose and reload of stdin table.
- Improve error handling.

## Version 1.9.9

Released on April 3, 2019

- Reorganize return code. Maybe finalized.

## Version 1.9.8

Released on April 2, 2019

- Improve signal handling to catch SIGINT, SIGQUIT, SIGTERM on several systems.
- Fix the following bug.
  - Ordinal placeholders in select clause not recognized correctly.

## Version 1.9.7

Released on April 1, 2019

- Fix syntax text.
- Improve mutual exclusion.

## Version 1.9.6

Released on March 30, 2019

- Support SELECT FOR UPDATE syntax.
- Support cursor with a prepared statement.
- Make update operations safety in parallel processing.

## Version 1.9.5

Released on March 24, 2019

- Make file operations safety in parallel processing.
- Modify the return code of external command errors from 16 to 1.
- Fix the following bug.
  - Temporary tables have not been affected by transactions. 

## Version 1.9.4

Released on March 22, 2019

- Support prepared statement syntax.
- Organize the command return code.

## Version 1.9.3

Released on March 20, 2019

- Refactor and modify internal processes for [csvq-driver](https://github.com/mithrandie/csvq-driver).
  
  This update does not include any functional changes to the csvq command except for bugs that will be found in the future.

## Version 1.9.1

Released on March 4, 2019

- Fix the following bug.
  - JSON parsing error in numeric values.

## Version 1.9.0

Released on March 3, 2019

- Add the following command options.
  - import-format
  - delimiter-positions
  - write-delimiter-positions

- Modify the behavior of the following command options.
  - delimiter
  - write-delimiter

- Add the following table attribute.
  - DELIMITER_POSITIONS

- Make the Table Object Expressions available in the following statements.
  - Insert Query
  - Update Query
  - Alter Table Query
  - Show Fields Statement

## Version 1.8.6

Released on February 27, 2019

- Modify ".txt" to be an extension that does not link to any file format. ([Github #5](https://github.com/mithrandie/csvq/issues/5))

## Version 1.8.5

Released on February 25, 2019

- Support UTF-8 with Byte order mark.
- Support Single-Line Fixed-Length Format.

## Version 1.8.4

Released on February 22, 2019

- Implement Check Update subcommand.

## Version 1.8.3

Released on February 17, 2019

- Fix the following bugs.
  - RETURN statement does not return a value in IF and WHILE statements.
  - NOW Function returns different time from the specification in user-defined functions.

## Version 1.8.2

Released on February 13, 2019

- Fix the following bug.
  - Panic occurs when an empty environment variable is passed. ([Github #4](https://github.com/mithrandie/csvq/pull/4))

## Version 1.8.1

Released on January 6, 2019

- Improve completer.
- Fix some bugs of completer.

## Version 1.8.0

Released on December 31, 2018

- Support LTSV Format.

## Version 1.7.3

Released on December 27, 2018

- Improve logics of parallel routine.

## Version 1.7.2

Released on December 25, 2018

- Implement Syntax subcommand.

## Version 1.7.1

Released on December 15, 2018

- Fix the following bugs.
  - TableObject does not accept identifier as an argument.

## Version 1.7.0

Released on December 14, 2018

- Enhance the interactive shell.
  - Completion (default: true)
  - Kill Whole Line (default: false)
  - Vi-mode (default: false)

## Version 1.6.7

Released on December 2, 2018

- Fork github.com/chzyer/readline and change dependency to github.com/mithrandie/readline-csvq to use the latest update that is not versioned. 

## Version 1.6.6

Released on November 27, 2018

- Fix fatal error of variable substitution in multithreading.

## Version 1.6.5

Released on November 26, 2018

- Implement Flag Related Statements.
  - ADD FLAG ELEMENT
  - REMOVE FLAG ELEMENT
- Fix a bug of datetime formats configuration.

## Version 1.6.4

Released on November 25, 2018

- Implement Identical Operator ("==").

## Version 1.6.3

Released on November 24, 2018

- Fix the following bug.
  - Color output is on by default. ([Github #3](https://github.com/mithrandie/csvq/issues/3))

## Version 1.6.2

Released on November 24, 2018

- Implement run-external-command statement.
- Add value expressions.
  - Runtime Information.
- Add built-in commands.
  - ECHO
  - CHDIR
  - PWD
  - RELOAD CONFIG
- Add configuration items to csvq_env.json.
  - interactive_shell.prompt
  - interactive_shell.continuous_prompt
- Bug Fixes

## Version 1.6.1

Released on November 19, 2018

- Fix a bug of colorize in JSON pretty print.
- Add --json-escape option.

## Version 1.6.0

Released on November 18, 2018

- Support Environment Variables.
- Support Configuration Files and Pre-Load Statements.
- Add command options.
  - enclose-all
  - east-asian-encoding
  - count-diacritical-sign
  - count-format-code

## Version 1.5.4

Released on November 9, 2018

- Fix a bug of string format.

## Version 1.5.3

Released on November 8, 2018

- Implement EXECUTE statement.
- Implement NUMBER_FORMAT function.
- Make FORMAT function to determine the number of digits automatically when precision is not specified.

## Version 1.5.2

Released on November 5, 2018

- Fix a bug in calculation of Shift_JIS byte length.

## Version 1.5.1

Released on November 5, 2018

- Fix a bug of interactive shell that hide query results when the --out option is specified.

## Version 1.5.0

Released on November 4, 2018

- Support Fixed-Length Format.
- Implement WIDTH function.
- Support operate with byte length in Shift_JIS encoding in the following string functions.
  - BYTE_LEN
  - LPAN
  - RPAD
- Implement ALTER TABLE SET ATTRIBUTE statement.


## Version 1.4.3

Released on October 20, 2018

- Fix return code when on-usage-error occurred.
- Add flags for write out.

## Version 1.4.2

Released on October 18, 2018

- Fix output format problems on the specifications.
  - Conversion to GigHub Flavored Markdown Format
    - Ternary -> bool or empty string
    - Null -> empty string
  - Conversion to Org-mode Table Format
    - Ternary -> bool or empty string
    - Null -> empty string

## Version 1.4.1

Released on October 18, 2018

- Fix a bug of datetime conversion.

## Version 1.4.0

Released on October 16, 2018

- Add output formats.
  - Text Table for GitHub Flavored Markdown
  - Text Table for Emacs Org-mode

## Version 1.3.1

Released on October 14, 2018

- Fix a bug of output ANSI escape sequence.

## Version 1.3.0

Released on October 14, 2018

- Support ANSI escape sequence.
- Enhance support for JSON.
  - Load data from a JSON file with the JSON_TABLE expression in From Clause.
  - Load data from a JSON data from standard input with the –json-query option.
  - Export a result of a select query in JSON format with the –format option.
  - Load a value from a JSON data using functions.
    - JSON_VALUE
    - JSON_OBJECT
    - JSON_AGG (Aggregate Function)
    - JSON_AGG (Analytic Function)
  - Load a row value from a JSON data using the JSON_ROW expression.

## Version 1.2.0

Released on September 26, 2018

- Support for Go 1.11 Modules.

## Version 1.1.1

Released on April 5, 2018

- Implement string functions.
  - INSTR
  - LIST_ELEM

## Version 1.1.0

Released on March 1, 2018

- Support for Go 1.10

## Version 1.0.2

Released on December 8, 2017

- Fix some bugs of operetor precedence.

## Version 1.0.1

Released on September 26, 2017

- Implement DISPOSE FUNCTION statement.
- Implement windowing clause in analytic function.

## Version 1.0.0

Released on September 19, 2017

The first general release. 