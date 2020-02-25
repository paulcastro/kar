const express = require('express')
const { logger, preprocessor, postprocessor } = require('./kar')

const app = express()

app.use(logger, preprocessor)

app.post('/incr', (req, res) => {
  res.json(req.body + 1)
})

app.use(postprocessor)

app.listen(process.env.KAR_APP_PORT)
