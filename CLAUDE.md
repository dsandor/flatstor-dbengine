# flatstor dbengine

This project implements a database engine that uses TSQL style queries in order to query data. The data will be stored in an AWS S3 bucket. The data will be stored in multiple files for a row to support massively wide data sets such as a financial instrument that has 10,000+ columns of data. 

Considerations for data storage:

Data must be updatable quickly.
Columns must be indexable.

A single row of data should be a O1 type lookup. Possibly store the data in a TRIE type structure to support this. 
Indexed columnar data should be searchable quickly.

Focus on high performance and data update simplicity so the backend data files can be updated separately. 

Support columns that overlap but are addressable by column group name.  For example, a financial instrument may have ISIN defined from Bloomberg data, ICE data, and some other data sources. I should be able to query all the columns of data for one or more instruments (rows). I should also be able to query and return any column of data with the row.

Example column layout:

| Common       | Bloomberg                | ICE           |
| InstrumentID | ISIN | BBGLOBAL | Ticker | ISIN | Ticker |
|--------------|------|----------|--------|------|--------|
| abc123_guid  | AAA1 | AAA1BBG  | AAPL   | AAA1 | AAPL-US|

Example Query:

SELECT InstrumentID, Bloomberg.ISIN, Bloomberg.Ticker, ICE.ISIN
FROM asset_table
WHERE Bloomberg.Ticker = 'AAPL'

or

SELECT * FROM asset_table WHERE InstrumentID = 'abc123_guid'

Either implement a sql engine or leverage a technology such as duckdb but enhance it to support the massively wide data set and options to store the file data on disk or in an s3 bucket.  

## Storeage Implementation

Initially support the disk only persistence but in such a way that adding S3 storage support later will be an option.

