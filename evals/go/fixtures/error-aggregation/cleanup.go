package cleanup

func Run(callbacks ...func() error) error {
	var result error
	for _, callback := range callbacks {
		if err := callback(); err != nil {
			result = err
		}
	}
	return result
}
