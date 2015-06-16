# mailpost
Mailpost is intended to be used as a method to post to a Hugo blog via email.

Mailpost will retrieve emails from a specified email address, save and attached or referenced images, update the image references in the text and save the text as a markdown document.

It is intended to be used as a method to post to a Hugo blog via email.

The ImageDir and PostDir values in the config file specifies the location to save posts and images. The string "<date>" will be replaced with the date the email is received for images and will be replaced with the value of "date" in the post's frontmatter for a post.

Also, the string "<type>" used in PostDir will be replaced with the "type" specified in the post's frontmatter. 

If PostFrom is set in the config file. Only emails from that email address will be parsed.  Others will be ignored.

Attached images or images referenced with a URL will also be saved
and markdown references to them will be changed to point to the
locally saved images.

For example, an email has an attached image named "apple.jpg" and the text part of the email contains some valid image markdown: ```![An apple](apple.jpg "This is the apple.")```
		
The apple.jpg file will be saved locally and the markdown will be updated to point to the image at your site (example.com) using the directory and path information provided in the config file: ```![An apple](http:example.com/media/images/apple.jpg "This is the apple.")```