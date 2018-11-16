FROM scratch

COPY bin/aws-asg-roller-linux-amd64 /aws-asg-roller

CMD ["/aws-asg-roller"]
