---
name: spreadsheets
description: Read and update tabular sheets, rows, cells, and numeric summaries.
---
Use the sheet id and column names returned by spreadsheets-tools_sheet_list or spreadsheets-tools_sheet_read_rows. Cell updates are exact and do not reorder rows. When appending a row, encode its scalar cell values as a JSON object; numeric cells are stored as text.
