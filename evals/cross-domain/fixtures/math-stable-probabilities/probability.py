import math


def logsumexp(values):
    return math.log(sum(math.exp(value) for value in values))


def softmax(values):
    denominator = sum(math.exp(value) for value in values)
    return [math.exp(value) / denominator for value in values]
